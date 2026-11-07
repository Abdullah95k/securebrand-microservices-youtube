package relay

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

// Config is how to reach Kafka: the platform's credentials
// (platform-credentials), one file per value.
type Config struct {
	Brokers   []string
	User      string
	Password  string
	Mechanism string // SCRAM-SHA-256 (the platform's), SCRAM-SHA-512 or PLAIN
	TLS       bool
	CA        []byte // PEM; empty: the system's roots
}

// The platform's file names.
const (
	fileBrokers   = "KAFKA_BROKERS"
	fileUser      = "KAFKA_USER"
	filePassword  = "KAFKA_PASSWORD"
	fileMechanism = "KAFKA_SASL_MECHANISM"
	fileTLS       = "KAFKA_TLS"
	fileCA        = "KAFKA_CA_CERT"
)

// LoadConfig reads the Kafka settings from dir. The brokers are required;
// the user and password together, or neither; TLS is on when KAFKA_TLS
// says so or a CA is given.
func LoadConfig(dir string) (Config, error) {
	var c Config
	read := func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return secrets.ReadFile(p)
	}
	brokers, err := read(fileBrokers)
	if err != nil {
		return c, err
	}
	for _, b := range strings.Split(brokers, ",") {
		if b = strings.TrimSpace(b); b != "" {
			c.Brokers = append(c.Brokers, b)
		}
	}
	if len(c.Brokers) == 0 {
		return c, fmt.Errorf("relay: no brokers in %s", filepath.Join(dir, fileBrokers))
	}
	if c.User, err = read(fileUser); err != nil {
		return c, err
	}
	if c.Password, err = read(filePassword); err != nil {
		return c, err
	}
	if (c.User == "") != (c.Password == "") {
		return c, errors.New("relay: KAFKA_USER and KAFKA_PASSWORD go together")
	}
	if c.Mechanism, err = read(fileMechanism); err != nil {
		return c, err
	}
	tlsOn, err := read(fileTLS)
	if err != nil {
		return c, err
	}
	switch strings.ToLower(tlsOn) {
	case "true", "1", "yes", "on":
		c.TLS = true
	case "", "false", "0", "no", "off":
	default:
		return c, fmt.Errorf("relay: KAFKA_TLS %q: want true or false", tlsOn)
	}
	ca, err := read(fileCA)
	if err != nil {
		return c, err
	}
	if ca != "" {
		c.CA, c.TLS = []byte(ca), true
	}
	return c, c.check()
}

// check refuses credentials in the clear to another machine.
func (c Config) check() error {
	if c.User == "" || c.TLS {
		return nil
	}
	for _, b := range c.Brokers {
		host, _, err := net.SplitHostPort(b)
		if err != nil {
			host = b
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("relay: Kafka credentials without TLS to %s: set KAFKA_TLS or KAFKA_CA_CERT", host)
		}
	}
	return nil
}

func (c Config) mechanism() (sasl.Mechanism, error) {
	switch strings.ToUpper(c.Mechanism) {
	case "", "SCRAM-SHA-256":
		return scram.Auth{User: c.User, Pass: c.Password}.AsSha256Mechanism(), nil
	case "SCRAM-SHA-512":
		return scram.Auth{User: c.User, Pass: c.Password}.AsSha512Mechanism(), nil
	case "PLAIN":
		return plain.Auth{User: c.User, Pass: c.Password}.AsMechanism(), nil
	}
	return nil, fmt.Errorf("relay: SASL mechanism %q: want SCRAM-SHA-256, SCRAM-SHA-512 or PLAIN", c.Mechanism)
}

// TransactionalID is the relay's Kafka transactional ID: one relay sends at
// a time (an advisory lock in Postgres), and a new one fences the old.
const TransactionalID = "yts-relay"

// Options are the Kafka client's options: transactional and idempotent,
// never creating a topic (the platform owns them), keys hashed as Kafka's
// own clients do so a post's messages share a partition.
func (c Config) Options() ([]kgo.Opt, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(c.Brokers...),
		kgo.TransactionalID(TransactionalID),
		kgo.TransactionTimeout(time.Minute),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchMaxBytes(1 << 20),
		kgo.RecordDeliveryTimeout(2 * time.Minute),
		kgo.ClientID("yts-relay"),
	}
	if c.User != "" {
		m, err := c.mechanism()
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.SASL(m))
	}
	if c.TLS {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if len(c.CA) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(c.CA) {
				return nil, errors.New("relay: KAFKA_CA_CERT holds no certificate")
			}
			cfg.RootCAs = pool
		}
		opts = append(opts, kgo.DialTLSConfig(cfg))
	}
	return opts, nil
}
