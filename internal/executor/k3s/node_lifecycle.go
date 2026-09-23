package k3s

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrNodeStillPresent = errors.New("Kubernetes Node is still present")
	ErrDrainTimeout     = errors.New("Kubernetes Node drain timed out")
)

type podReference struct {
	Namespace string
	Name      string
	Phase     string
	Mirror    bool
	DaemonSet bool
}

type podListResponse struct {
	Items []struct {
		Metadata struct {
			Name            string            `json:"name"`
			Namespace       string            `json:"namespace"`
			Annotations     map[string]string `json:"annotations"`
			OwnerReferences []struct {
				Kind string `json:"kind"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	} `json:"items"`
}

// Cordon marks a worker Node unschedulable. It is idempotent because the
// patch sets the desired value rather than toggling the current value.
func (r *KubernetesRuntime) Cordon(ctx context.Context, node string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	if strings.TrimSpace(node) == "" {
		return errors.New("Kubernetes Node name is required")
	}
	path := r.path("api/v1/nodes/" + url.PathEscape(node))
	err := r.doJSON(ctx, http.MethodPatch, path, map[string]any{"spec": map[string]bool{"unschedulable": true}}, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Drain cordons first, then deletes only ordinary non-terminal Pods assigned
// to the Node. Mirror and DaemonSet Pods are left for the cluster to manage.
func (r *KubernetesRuntime) Drain(ctx context.Context, node string) error {
	if err := r.Cordon(ctx, node); err != nil {
		return err
	}
	timeout := r.config.DrainTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := r.config.DrainPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		pods, err := r.podsOnNode(ctx, node)
		if err != nil {
			return err
		}
		remaining := 0
		for _, pod := range pods {
			if pod.Mirror || pod.DaemonSet || pod.Phase == "Succeeded" || pod.Phase == "Failed" {
				continue
			}
			remaining++
			podPath := r.path("api/v1/namespaces/" + url.PathEscape(pod.Namespace) + "/pods/" + url.PathEscape(pod.Name))
			if err := r.doJSON(ctx, http.MethodDelete, podPath, nil, nil); err != nil && !isNotFound(err) {
				return err
			}
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrDrainTimeout
		case <-time.After(interval):
		}
	}
}

// CleanupExecution removes only the deterministic Job and Pods whose labels
// and owner references bind them to the requested task and attempt. It waits
// until both Job and Pods are absent before node teardown may proceed.
func (r *KubernetesRuntime) CleanupExecution(ctx context.Context, taskID, attemptID string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	if !validLabelValue(taskID) || !validLabelValue(attemptID) {
		return errors.New("task and attempt IDs are required Kubernetes label values")
	}
	id := jobName(taskID, attemptID)
	jobPath := r.path("apis/batch/v1/namespaces/" + url.PathEscape(r.config.Namespace) + "/jobs/" + url.PathEscape(id))
	var job kJob
	err := r.doJSON(ctx, http.MethodGet, jobPath, nil, &job)
	jobUID := ""
	if err == nil {
		if job.Metadata.Name != id || job.Metadata.UID == "" || !sameLabels(job.Metadata.Labels, map[string]string{"task_id": taskID, "attempt_id": attemptID, "executor": executorName}) {
			return errors.New("Kubernetes execution identity mismatch during cleanup")
		}
		jobUID = job.Metadata.UID
		err = r.deleteWithUID(ctx, jobPath, job.Metadata.UID, "Foreground")
		if err != nil && !isNotFound(err) {
			return err
		}
	} else if !isNotFound(err) {
		return err
	}

	timeout := r.config.DrainTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := r.config.DrainPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	selector := url.QueryEscape("job-name=" + id + ",task_id=" + taskID + ",attempt_id=" + attemptID)
	for {
		var current kJob
		jobErr := r.doJSON(ctx, http.MethodGet, jobPath, nil, &current)
		if jobErr != nil && !isNotFound(jobErr) {
			return jobErr
		}
		if jobErr == nil {
			if jobUID == "" || current.Metadata.Name != id || current.Metadata.UID != jobUID || !sameLabels(current.Metadata.Labels, map[string]string{"task_id": taskID, "attempt_id": attemptID, "executor": executorName}) {
				return errors.New("Kubernetes execution identity changed during cleanup")
			}
		}
		var pods kPodList
		podsErr := r.doJSON(ctx, http.MethodGet, r.path("api/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/pods?labelSelector="+selector), nil, &pods)
		if podsErr != nil {
			return podsErr
		}
		for _, pod := range pods.Items {
			if pod.Metadata.Name == "" || pod.Metadata.UID == "" || !sameLabels(pod.Metadata.Labels, map[string]string{"task_id": taskID, "attempt_id": attemptID, "executor": executorName}) {
				return errors.New("Kubernetes worker Pod identity mismatch during cleanup")
			}
			ownedByJob := false
			for _, owner := range pod.Metadata.OwnerReferences {
				if owner.Kind == "Job" && owner.Name == id {
					ownedByJob = true
					break
				}
			}
			if !ownedByJob {
				return errors.New("Kubernetes worker Pod owner mismatch during cleanup")
			}
			if jobUID == "" {
				// A Pod found after its Job was already absent has no durable
				// owner identity to compare against. Let Kubernetes GC remove it;
				// never delete a same-name resource based on labels alone.
				continue
			}
			ownerUIDMatches := false
			for _, owner := range pod.Metadata.OwnerReferences {
				if owner.Kind == "Job" && owner.Name == id && owner.UID == jobUID {
					ownerUIDMatches = true
					break
				}
			}
			if !ownerUIDMatches {
				return errors.New("Kubernetes worker Pod owner UID mismatch during cleanup")
			}
			podPath := r.path("api/v1/namespaces/" + url.PathEscape(r.config.Namespace) + "/pods/" + url.PathEscape(pod.Metadata.Name))
			if err := r.deleteWithUID(ctx, podPath, pod.Metadata.UID, ""); err != nil && !isNotFound(err) {
				return err
			}
		}
		if isNotFound(jobErr) && len(pods.Items) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Kubernetes execution resources remain for task %s attempt %s", taskID, attemptID)
		case <-ticker.C:
		}
	}
}

func (r *KubernetesRuntime) RemoveNode(ctx context.Context, node string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	if strings.TrimSpace(node) == "" {
		return errors.New("Kubernetes Node name is required")
	}
	err := r.doJSON(ctx, http.MethodDelete, r.path("api/v1/nodes/"+url.PathEscape(node)), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (r *KubernetesRuntime) VerifyNodeRemoved(ctx context.Context, node string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	timeout := r.config.DrainTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := r.config.DrainPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		var response map[string]any
		err := r.doJSON(ctx, http.MethodGet, r.path("api/v1/nodes/"+url.PathEscape(node)), nil, &response)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := r.RemoveNode(ctx, node); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: %s", ErrNodeStillPresent, node)
		case <-ticker.C:
		}
	}
}

func (r *KubernetesRuntime) podsOnNode(ctx context.Context, node string) ([]podReference, error) {
	var response podListResponse
	selector := url.QueryEscape("spec.nodeName=" + node)
	if err := r.doJSON(ctx, http.MethodGet, r.path("api/v1/pods?fieldSelector="+selector), nil, &response); err != nil {
		return nil, err
	}
	pods := make([]podReference, 0, len(response.Items))
	for _, item := range response.Items {
		pod := podReference{Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, Phase: item.Status.Phase}
		_, pod.Mirror = item.Metadata.Annotations["kubernetes.io/config.mirror"]
		for _, owner := range item.Metadata.OwnerReferences {
			if owner.Kind == "DaemonSet" {
				pod.DaemonSet = true
				break
			}
		}
		if pod.Namespace == "" || pod.Name == "" {
			return nil, errors.New("Kubernetes Pod list contained an incomplete identity")
		}
		pods = append(pods, pod)
	}
	return pods, nil
}
