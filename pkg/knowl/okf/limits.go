package okf

// Limits bounds parsing and rendering work.
type Limits struct {
	MaxBytes   int
	MaxNodes   int
	MaxAliases int
	MaxDepth   int
}

// DefaultLimits returns conservative per-document limits within Knowl's
// existing canonical page bounds.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:   256 << 10,
		MaxNodes:   16_384,
		MaxAliases: 32,
		MaxDepth:   64,
	}
}

func (limits Limits) valid() bool {
	return limits.MaxBytes > 0 && limits.MaxBytes <= 64<<20 &&
		limits.MaxNodes > 0 && limits.MaxNodes <= 1_000_000 &&
		limits.MaxAliases >= 0 && limits.MaxAliases <= 10_000 &&
		limits.MaxDepth > 0 && limits.MaxDepth <= 256
}
