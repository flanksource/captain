package attachments

const (
	DefaultMaxFileBytes    int64 = 20 << 20
	DefaultMaxRequestBytes int64 = 50 << 20
	DefaultMaxFiles              = 10
)

type Limits struct {
	MaxFileBytes    int64
	MaxRequestBytes int64
	MaxFiles        int
}

func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:    DefaultMaxFileBytes,
		MaxRequestBytes: DefaultMaxRequestBytes,
		MaxFiles:        DefaultMaxFiles,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = defaults.MaxFileBytes
	}
	if l.MaxRequestBytes == 0 {
		l.MaxRequestBytes = defaults.MaxRequestBytes
	}
	if l.MaxFiles == 0 {
		l.MaxFiles = defaults.MaxFiles
	}
	return l
}

func (l Limits) validate() error {
	if l.MaxFileBytes < 1 {
		return invalidLimit("max file bytes", l.MaxFileBytes)
	}
	if l.MaxRequestBytes < 1 {
		return invalidLimit("max request bytes", l.MaxRequestBytes)
	}
	if l.MaxFiles < 1 {
		return invalidLimit("max files", l.MaxFiles)
	}
	return nil
}
