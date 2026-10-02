package sessionruntime

import (
	"errors"
	"os"
)

const bootstrapCodex0160Version = "0.160.0"

// The archive SHA256 is verified against OpenAI's codex-package_SHA256SUMS.
// These normalized file hashes were generated from that exact archive after
// converting package modes to the installer contract (0644/0755).
var bootstrapCodex0160Bundle = codexArtifactBundle{
	version: bootstrapCodex0160Version,
	target:  bootstrapCodex0155Target,
	files: []codexArtifactFile{
		{path: "bin/codex", sha256: "12eb3e81114588aca3b7998f4f19e8997b056aca08e57a7ca7c8a3ec8c652aad", mode: 0755},
		{path: "bin/codex-code-mode-host", sha256: "37cab1584302611e9936902219640ab5e7a79fcfccd2504c6e85ea8cb97d0e10", mode: 0755},
		{path: "codex-package.json", sha256: "2aa6a23f733c278a9bf385f0725b97b76708559d31209636cb165c1c872a40ef", mode: 0644},
		{path: "codex-path/rg", sha256: "e62198eb19b136b88c330af83647b5a962cb99b6b1f066758568f12de1974849", mode: 0755},
		{path: "codex-resources/bwrap", sha256: "01fb705f067bd5365b63d8ad2323a61c8d007733ca5e649437e086f3fb9935d8", mode: 0755},
		{path: "codex-resources/voice/NOTICE.md", sha256: "40097a1799441051aa0b6645cdc819cceb19be73cdd265eb602aa2b15e0b1f48", mode: 0644},
		{path: "codex-resources/voice/bin/codex-voice-host", sha256: "b5b871df347f59b4cf5f82f438caa054ccfaa6a0d5c00cddbfb830655ab4ed9f", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstapp.so", sha256: "d9e4f502e5c2aeb2e9470d5efaa5a8657cb9bacddce106bc88facea92becb366", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstaudioconvert.so", sha256: "683202e93824c36fa7acecd9de6df2ad891110ebbc959baebe02ed4e873d3c50", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstaudioresample.so", sha256: "fffe83239de05df3f6acaf9203231b1b37ab39fe17aaf4cf8516de99aa5ca1ba", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstcoreelements.so", sha256: "44356d5e6867acdd76966aa5395428f63b4954389e85e0f748564d5989422aa1", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstopus.so", sha256: "39874376534b8d5f72fe6bbe62f4df258e33dba76a2f66bf5f3389940aa4f752", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstrtp.so", sha256: "7a4599d312c3a00a1794f9b99481ac147cee7554b6d36d1479df6b7fd019b2a8", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstrtpmanager.so", sha256: "c1f55dad0daedc45b189e970a46519bb065420f704c2265d95e040686b6adebb", mode: 0755},
		{path: "codex-resources/voice/lib/libffi.so.8", sha256: "6ef5db59b2e4b1eb63032fa6c8afb1669bc09b073789233c379efda85b1353db", mode: 0755},
		{path: "codex-resources/voice/lib/libgio-2.0.so.0", sha256: "3b3cc57ae7b680ba5efd0ff9fdcabb9fe324a76d2242762ba6ac3b6240e1a7ab", mode: 0755},
		{path: "codex-resources/voice/lib/libglib-2.0.so.0", sha256: "edfa7989861b231b46b564dbad9e0445bd6788e088f1ab60f0a85c4a61428a44", mode: 0755},
		{path: "codex-resources/voice/lib/libgmodule-2.0.so.0", sha256: "e8e87e59f24569ad3c20b039eefce03aea811eee10352904e33b726e53f8d22f", mode: 0755},
		{path: "codex-resources/voice/lib/libgobject-2.0.so.0", sha256: "5e3fcf7f22575d0c3295589ae44d07b3bef234aa18ee8aea45317a7380783b16", mode: 0755},
		{path: "codex-resources/voice/lib/libgstallocators-1.0.so.0", sha256: "43873f69fa9e165c375ad9bfe3b13a12610c34765a4fed2a734c1a174e9ffa74", mode: 0755},
		{path: "codex-resources/voice/lib/libgstapp-1.0.so.0", sha256: "0b06343f03ac90e03345ceee1a69121676bead76b87df9c2c3ebadb3acc5cdc2", mode: 0755},
		{path: "codex-resources/voice/lib/libgstaudio-1.0.so.0", sha256: "864c5ee92de639bc699c354b548eab085aa222e87e0051918db31c86b741d5d4", mode: 0755},
		{path: "codex-resources/voice/lib/libgstbase-1.0.so.0", sha256: "e215a5fcd03f52f4492a898a582217d8033c32e730f9221989c9e19032b99a6f", mode: 0755},
		{path: "codex-resources/voice/lib/libgstnet-1.0.so.0", sha256: "9d19251c9b72b9c9049cc157f9a921fdc527111b0a4d349d87385a7bd3d2663f", mode: 0755},
		{path: "codex-resources/voice/lib/libgstpbutils-1.0.so.0", sha256: "b07f4b00770e3f9a5700803374995717ad76b7f045bd15b4d401f3b83d2d379f", mode: 0755},
		{path: "codex-resources/voice/lib/libgstreamer-1.0.so.0", sha256: "ebcbaaaad2bf51f483cd543aa3579cb77b8a128b835152a1481429324fbe98be", mode: 0755},
		{path: "codex-resources/voice/lib/libgstrtp-1.0.so.0", sha256: "bb5d6207c907f0906fbc8a48262149be2953041f0124b556aa3ec2b511d4b97e", mode: 0755},
		{path: "codex-resources/voice/lib/libgsttag-1.0.so.0", sha256: "7b0931a2f006733cb7a2b5729b34dc5257b7f3e46cd8a2770bb0d01086af56c7", mode: 0755},
		{path: "codex-resources/voice/lib/libgstvideo-1.0.so.0", sha256: "7f10b05f6716115b5b2a7eb284dfc8136d653d940ba253d10d269fcfd7527cd7", mode: 0755},
		{path: "codex-resources/voice/lib/libintl.so.8", sha256: "f50f3ab34be7b3392f2db82bd67d50e133b35fa41d352dc3b3a97b70b0ffd960", mode: 0755},
		{path: "codex-resources/voice/lib/libopus.so.0", sha256: "a14170fac3704ed2ef79e030c6fc85ea7058a2d6e9f3cfacd8f47a6f63deb976", mode: 0755},
		{path: "codex-resources/voice/lib/libpcre2-8.so.0", sha256: "e39519b242f40686734ed8d0218f68cac24f0e956ec64c8c1b49b9da5cc9788b", mode: 0755},
		{path: "codex-resources/voice/lib/libz.so.1", sha256: "6c5d765f42d9368cc4bdbd11f725c1bcf6b14b78322033d3c94f787fc6261f5e", mode: 0755},
		{path: "codex-resources/voice/licenses/LGPL-2.1.txt", sha256: "ad2eec519ebd4b5df86ea84dff24ae3bfa2edea846a703b58902dd221ae375db", mode: 0644},
		{path: "codex-resources/voice/licenses/Opus.txt", sha256: "01e1167d54a096d123cf6dfbbeb19587278845c6481d2d66d545669846079551", mode: 0644},
		{path: "codex-resources/voice/licenses/PCRE2.md", sha256: "197d8a73ffee0d6b09adba2f9c677b5f5aede24edf89258a68e48248d010d811", mode: 0644},
		{path: "codex-resources/voice/licenses/libffi.txt", sha256: "17b64dc60f3b6897a60f971e288b973f655c2edcdf08b25f3c3dd5549857881c", mode: 0644},
		{path: "codex-resources/voice/licenses/proxy-libintl.txt", sha256: "d245807f90032872d1438d741ed21e2490e1175dc8aa3afa5ddb6c8e529b58e5", mode: 0644},
		{path: "codex-resources/voice/licenses/sljit.txt", sha256: "5f216505c0f6ea3273caec89e766eef93cdeb7bbb0c429f9360116d7c938feeb", mode: 0644},
		{path: "codex-resources/voice/licenses/zlib.txt", sha256: "e32ff4e00d9d94930537635291da39e7e612703334bf6fde8c7f1686fe8a45a2", mode: 0644},
		{path: "codex-resources/voice/manifest.json", sha256: "c7c90399f39b195d71685d6fe9d9b852bda9c1ede5305b19c79d8942f54b107d", mode: 0644},
		{path: "codex-resources/voice/runtime.json", sha256: "a64f6acd58efb50f606c43825393e3d28bdd6604e89abe0523c4e9a618795083", mode: 0755},
		{path: "codex-resources/voice/sources.json", sha256: "e2f6b124f6277e7c3c4f5c7cc8830c66190f671ad65e972bb2cc9507dbfcb937", mode: 0644},
		{path: "codex-resources/zsh/bin/zsh", sha256: "67faaaa89242c4a332e16e508a1977cffc24bf7fca31d4411cdfd101f3831ef3", mode: 0755},
	},
}

func bootstrapCodexBundleForVersion(version string) (codexArtifactBundle, error) {
	switch version {
	case bootstrapCodex0155Version:
		return bootstrapCodex0155Bundle, nil
	case bootstrapCodex0160Version:
		return bootstrapCodex0160Bundle, nil
	default:
		return codexArtifactBundle{}, errors.Join(errBootstrapArtifact, os.ErrNotExist)
	}
}
