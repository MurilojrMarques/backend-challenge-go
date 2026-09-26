package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MurilojrMarques/backend-challenge-go/migrations"
)

const (
	PostgresImage   = "postgres:17-alpine"
	KeycloakImage   = "quay.io/keycloak/keycloak:26.3"
	LocalStackImage = "localstack/localstack:4"

	DatabaseName     = "wallet"
	AdminUser        = "postgres"
	AdminPassword    = "postgres"
	MigratorUser     = "wallet_migrator"
	MigratorPassword = "wallet_migrator"
	AppUser          = "wallet_app"
	AppPassword      = "wallet_app"
)

func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

type Postgres struct {
	container testcontainers.Container
	host      string
	port      string
}

func StartPostgres(ctx context.Context) (*Postgres, error) {
	c, err := postgres.Run(ctx, PostgresImage,
		postgres.WithDatabase(DatabaseName),
		postgres.WithUsername(AdminUser),
		postgres.WithPassword(AdminPassword),
		postgres.WithInitScripts(filepath.Join(RepoRoot(), "deploy", "postgres", "001-roles.sh")),
		testcontainers.WithEnv(map[string]string{
			"WALLET_MIGRATOR_PASSWORD": MigratorPassword,
			"WALLET_APP_PASSWORD":      AppPassword,
		}),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres container: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	p := &Postgres{container: c, host: host, port: port.Port()}
	if err := p.MigrateUp(); err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	return p, nil
}

func (p *Postgres) URL(user, password string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, p.host, p.port, DatabaseName)
}

func (p *Postgres) AdminURL() string    { return p.URL(AdminUser, AdminPassword) }
func (p *Postgres) MigratorURL() string { return p.URL(MigratorUser, MigratorPassword) }
func (p *Postgres) AppURL() string      { return p.URL(AppUser, AppPassword) }

func (p *Postgres) Migrator() (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", source, "pgx5://"+strings.TrimPrefix(p.MigratorURL(), "postgres://"))
}

func (p *Postgres) MigrateUp() error {
	m, err := p.Migrator()
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func (p *Postgres) Terminate(ctx context.Context) error {
	return p.container.Terminate(ctx)
}

type Keycloak struct {
	container testcontainers.Container
	URL       string
}

func StartKeycloak(ctx context.Context) (*Keycloak, error) {
	c, err := testcontainers.Run(ctx, KeycloakImage,
		testcontainers.WithCmd("start-dev", "--import-realm"),
		testcontainers.WithEnv(map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
			"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin",
			"KC_HTTP_ENABLED":             "true",
			"KC_HEALTH_ENABLED":           "true",
			"KC_LOG_LEVEL":                "warn",
		}),
		testcontainers.WithExposedPorts("8080/tcp", "9000/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(RepoRoot(), "deploy", "keycloak", "realm-wallet.json"),
			ContainerFilePath: "/opt/keycloak/data/import/realm-wallet.json",
			FileMode:          0o644,
		}),
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute,
			wait.ForHTTP("/health/ready").WithPort("9000/tcp").WithStartupTimeout(3*time.Minute),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("keycloak container: %w", err)
	}
	url, err := c.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		return nil, err
	}
	return &Keycloak{container: c, URL: url}, nil
}

func (k *Keycloak) Issuer() string {
	return Issuer(k.URL)
}

func (k *Keycloak) Terminate(ctx context.Context) error {
	return k.container.Terminate(ctx)
}

type LocalStack struct {
	container testcontainers.Container
	URL       string
}

var initSucceeded = regexp.MustCompile(`"state":\s*"SUCCESSFUL"`)

func StartLocalStack(ctx context.Context) (*LocalStack, error) {
	c, err := localstack.Run(ctx, LocalStackImage,
		testcontainers.WithEnv(map[string]string{
			"SERVICES":                       "sqs,iam,sts",
			"EAGER_SERVICE_LOADING":          "1",
			"SQS_ENDPOINT_STRATEGY":          "off",
			"SQS_DISABLE_CLOUDWATCH_METRICS": "1",
			"SKIP_SSL_CERT_DOWNLOAD":         "1",
			"DISABLE_EVENTS":                 "1",
			"AWS_DEFAULT_REGION":             Region,
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(RepoRoot(), "deploy", "localstack", "init-aws.sh"),
			ContainerFilePath: "/etc/localstack/init/ready.d/init-aws.sh",
			FileMode:          0o755,
		}),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute,
			wait.ForHTTP("/_localstack/init/ready").WithPort("4566/tcp").WithStartupTimeout(2*time.Minute).
				WithResponseMatcher(func(body io.Reader) bool {
					raw, err := io.ReadAll(body)
					return err == nil && initSucceeded.Match(raw)
				}),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("localstack container: %w", err)
	}
	url, err := c.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return nil, err
	}
	return &LocalStack{container: c, URL: url}, nil
}

func (l *LocalStack) Queue(name string) string {
	return QueueURL(l.URL, name)
}

func (l *LocalStack) Terminate(ctx context.Context) error {
	return l.container.Terminate(ctx)
}
