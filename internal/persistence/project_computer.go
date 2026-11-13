package persistence

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrComputerBusy = errors.New("project computer is controlled by a user or another task")

type ProjectComputerStore interface {
	SetIdleMinutes(context.Context, string, int) error
	IdleDesktopCandidates(context.Context, string) ([]string, error)
	SetTakeoverPending(context.Context, string, bool) error
	SetTimeZone(context.Context, string, string) error
	ChromeImportProgress(context.Context, string, string) (map[string]int, error)
	RecordChromeProfile(context.Context, string, string, string, int) error
	PendingChromeImport(context.Context, string) (*models.ComputerChromeGrant, error)
	SaveWorkflow(context.Context, *models.ComputerWorkflow) error
	Workflows(context.Context, string) ([]models.ComputerWorkflow, error)
	Workflow(context.Context, string, string) (*models.ComputerWorkflow, error)
	DeleteWorkflow(context.Context, string, string) error
	SetEnabled(context.Context, string, bool) error
	RequestLogin(ctx context.Context, id, kind, url string) error
	RequestHandoff(ctx context.Context, id, kind, url, reason, expected string) error
	CompleteLogin(ctx context.Context, id string) error
	FinishHandoff(ctx context.Context, id string, cancelled bool) error
	SetToolTarget(ctx context.Context, id, target string) error
	ThreadAttachments(ctx context.Context, projectID string) ([]models.ChatAttachment, error)
	ThreadAttachment(ctx context.Context, projectID, attachmentID string) (*models.ChatAttachment, error)
	CreateThreadAttachment(ctx context.Context, projectID string, a *models.ChatAttachment) (taskAttachment bool, err error)
	Get(ctx context.Context, id string) (*models.ProjectComputer, error)
	Configure(ctx context.Context, id, provider string) error
	Hold(ctx context.Context, id string, held bool) error
	Reserve(ctx context.Context, id, owner string) error
	Release(ctx context.Context, id, owner string) error
	Renew(ctx context.Context, id, owner string) error
	ForTask(ctx context.Context, projectID, taskID string) (ProjectComputerStore, error)
	SessionsBusy(ctx context.Context, id string) (bool, error)
	TransferRoot(ctx context.Context, projectID string) (string, error)
	Attachments() AttachmentStore
	CreateChromeGrant(ctx context.Context, id, scope string, selections []models.ChromeCookieSelection, expires int64) (*models.ComputerChromeGrant, error)
	ChromeGrantsPage(ctx context.Context, id string, offset int) ([]models.ComputerChromeGrant, error)
	ChromeGrants(ctx context.Context, id string) ([]models.ComputerChromeGrant, error)
	ChromeGrant(ctx context.Context, id, grantID string) (*models.ComputerChromeGrant, error)
	RecordChromeInstall(ctx context.Context, id, grantID string) error
	RevokeChromeGrant(ctx context.Context, id, grantID string) error
	ChromeInstallations(ctx context.Context, id, grantID string) ([]string, error)
	ExpiredChromeInstallations(ctx context.Context, id string) ([]models.ComputerChromeGrant, error)
	ClearChromeInstall(ctx context.Context, id, grantID string) error
}
