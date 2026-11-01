package e2e

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
)

const backupRestoreLabel = "backup-restore"

// requireScalewayQualification reports a deliberate skip when the external
// qualification credentials are absent. A skipped external test is never a
// qualification pass; callers record it in their suite output.
func requireScalewayQualification() {
	if os.Getenv("SCALEWAY_ACCESS_KEY") == "" || os.Getenv("SCALEWAY_SECRET_KEY") == "" || os.Getenv("SCALEWAY_BUCKET") == "" {
		Skip("Scaleway qualification credentials are not configured; this is not a passing qualification result")
	}
}

var _ = Describe("Backup restore qualification", Label(backupRestoreLabel), func() {
	It("requires explicit external credentials for Scaleway qualification", func() {
		if os.Getenv("RUN_SCALEWAY_QUALIFICATION") != "true" {
			Skip(fmt.Sprintf("set RUN_SCALEWAY_QUALIFICATION=true to execute %s external qualification", backupRestoreLabel))
		}
		requireScalewayQualification()
	})
})
