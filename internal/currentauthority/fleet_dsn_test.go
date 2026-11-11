package currentauthority

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFleetPoolDSNPreservesClientCertificateMode(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "require"} {
		for _, dsn := range []string{
			"postgres://user:secret@localhost:55471/db?sslmode=prefer&sslcertmode=" + mode,
			"host=localhost port=55471 user=user password=secret dbname=db sslmode=prefer sslcertmode=" + mode,
		} {
			original, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal("fixture did not parse")
			}
			derived, err := readerDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := pgxpool.ParseConfig(derived)
			if err != nil {
				t.Fatal("reader did not parse")
			}
			writer := writerConfig(original)
			if reader.ConnConfig.SSLCertMode != mode || writer.ConnConfig.SSLCertMode != mode || original.ConnConfig.SSLCertMode != mode {
				t.Fatal("certificate mode changed")
			}
			if _, ok := reader.ConnConfig.RuntimeParams["sslcertmode"]; ok {
				t.Fatal("certificate mode treated as server parameter")
			}
		}
	}
	for _, dsn := range []string{
		"postgres://user:secret@localhost/db?sslcertmode=invalid",
		"postgres://user:secret@localhost/db?sslcertmode=",
		"host=localhost user=user password=secret sslcertmode='invalid'",
		"host=localhost user=user password=secret sslcertmode=''",
	} {
		if _, err := readerDSN(dsn); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("invalid mode accepted: %v", err)
		}
	}
}
