//go:build race

package kernel

// raceEnabled reports whether the race detector is instrumenting this binary.
// It costs a 10–30× slowdown, which makes wall-clock assertions meaningless.
const raceEnabled = true
