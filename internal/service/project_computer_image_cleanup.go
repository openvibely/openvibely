package service

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
)

var managedComputerImage = regexp.MustCompile(`^openvibely-computer:[0-9a-f]{16}$`)

// Remove only obsolete version tags created by OV. Never force removal: Docker
// remains the final guard against deleting an image used by a container.
func (p *LocalComputerProvisioner) cleanupImages(ctx context.Context) error {
	select {
	case computerImageBuildLock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-computerImageBuildLock }()
	current := computerImageTag()
	override := os.Getenv("OPENVIBELY_COMPUTER_IMAGE")
	raw, err := p.docker(ctx, nil, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", "openvibely-computer")
	if err != nil {
		return err
	}
	var failures []error
	for _, image := range strings.Fields(string(raw)) {
		if !managedComputerImage.MatchString(image) || image == current || image == override {
			continue
		}
		// --all includes stopped containers, whose saved environment must remain usable.
		used, err := p.docker(ctx, nil, "container", "ls", "--all", "--filter", "ancestor="+image, "--format", "{{.ID}}")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if strings.TrimSpace(string(used)) != "" {
			continue
		}
		if _, err := p.docker(ctx, nil, "image", "rm", image); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
