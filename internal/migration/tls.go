package migration

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func ServerTLSConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	pool, cert, err := loadTLSMaterial(caPath, certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

func ClientTLSConfig(caPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	pool, cert, err := loadTLSMaterial(caPath, certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}, nil
}

func loadTLSMaterial(caPath, certPath, keyPath string) (*x509.CertPool, tls.Certificate, error) {
	for name, value := range map[string]string{"CA": caPath, "certificate": certPath, "key": keyPath} {
		if value == "" {
			return nil, tls.Certificate{}, fmt.Errorf("migration TLS %s path is required", name)
		}
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("read migration CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, tls.Certificate{}, fmt.Errorf("migration CA %s contains no certificates", caPath)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("load migration keypair: %w", err)
	}
	return pool, cert, nil
}
