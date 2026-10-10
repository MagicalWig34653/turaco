package wiring

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/clamav"
	knowledgepublic "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/public"
	deskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage/fsstore"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage/s3store"
)

// Attachments builds the attachment service (ADR-0037): the encrypted storage over the configured adapter, the
// ClamAV scanner and, when withOwners is set (the API), the authorization owners of Tickets and Knowledge
// articles. It returns nil when STORAGE_DRIVER is empty. The worker passes withOwners=false: it scans and purges
// but never authorizes users.
func Attachments(pool *pgxpool.Pool, cfg config.Config, sc config.StorageConfig, logger *slog.Logger, withOwners bool) (*attachments.Service, error) {
	if !sc.Enabled() {
		return nil, nil
	}
	var backend storage.Backend
	var err error
	switch sc.Driver {
	case config.StorageDriverFilesystem:
		backend, err = fsstore.New(sc.Path)
	case config.StorageDriverS3:
		backend, err = s3store.New(s3store.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, PathStyle: cfg.S3PathStyle})
	default:
		err = fmt.Errorf("unknown STORAGE_DRIVER %q", sc.Driver)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	key, err := storage.LoadMasterKey(sc.MasterKeyFile)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	vault, err := storage.NewVault(backend, key)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	policy, err := attachments.NewPolicy(sc.MaxBytes, sc.AllowedTypes)
	if err != nil {
		return nil, err
	}
	scanner, err := clamav.New(clamav.Config{Address: sc.ClamAVAddress, MaxBytes: sc.MaxBytes})
	if err != nil {
		return nil, err
	}
	svc := attachments.New(pool, vault, scanner, policy, logger)
	if withOwners {
		if err := svc.RegisterOwner(deskpublic.TicketAttachmentOwner, deskpublic.NewTicketAttachments(ServiceDesk(pool))); err != nil {
			return nil, err
		}
		if err := svc.RegisterOwner(knowledgepublic.ArticleAttachmentOwner, knowledgepublic.NewArticleAttachments(Knowledge(pool))); err != nil {
			return nil, err
		}
	}
	return svc, nil
}

// StorageHealth is the storage and scanner health input: the vault's backend check and the scan backlog facts.
type StorageHealth struct {
	Enabled bool
	Driver  string
	Svc     *attachments.Service
	Backend storage.Backend
}
