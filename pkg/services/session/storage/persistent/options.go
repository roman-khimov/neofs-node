package persistent

import (
	"crypto/ecdsa"
	"time"

	"github.com/nspcc-dev/neofs-node/pkg/util/logger"
	"go.uber.org/zap"
)

type cfg struct {
	l          *logger.Logger
	timeout    time.Duration
	privateKey *ecdsa.PrivateKey
}

// Option allows setting optional parameters of the TokenStore.
type Option func(*cfg)

func defaultCfg() *cfg {
	return &cfg{
		l:       &logger.Logger{Logger: zap.L()},
		timeout: 100 * time.Millisecond,
	}
}

// WithLogger returns an option to specify
// logger.
func WithLogger(v *logger.Logger) Option {
	return func(c *cfg) {
		c.l = v
	}
}

// WithTimeout returns option to specify
// database connection timeout.
func WithTimeout(v time.Duration) Option {
	return func(c *cfg) {
		c.timeout = v
	}
}

// WithEncryptionKey return an option to encrypt private
// session keys using provided private key.
func WithEncryptionKey(k *ecdsa.PrivateKey) Option {
	return func(c *cfg) {
		c.privateKey = k
	}
}
