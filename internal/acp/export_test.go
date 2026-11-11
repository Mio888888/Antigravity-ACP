package acp

func SetContextSizeProbeForTest(fn func(bin string) bool) (restore func()) {
	contextSizeProbeMu.Lock()
	prev := contextSizeProbe
	contextSizeProbe = fn
	contextSizeProbeCache = map[string]bool{}
	contextSizeProbeMu.Unlock()
	return func() {
		contextSizeProbeMu.Lock()
		contextSizeProbe = prev
		contextSizeProbeCache = map[string]bool{}
		contextSizeProbeMu.Unlock()
	}
}
