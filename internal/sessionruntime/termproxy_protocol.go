package sessionruntime

import (
	"fmt"
	"strconv"
)

func encodeTermproxyInput(input string) ([]byte, error) {
	return []byte("0:" + strconv.Itoa(len([]byte(input))) + ":" + input), nil
}

func encodeTermproxyResize(columns, rows int) ([]byte, error) {
	if columns < 1 || rows < 1 {
		return nil, fmt.Errorf("terminal dimensions must be positive")
	}
	return []byte("1:" + strconv.Itoa(columns) + ":" + strconv.Itoa(rows) + ":"), nil
}

func termproxyPingFrame() []byte {
	return []byte("2")
}
