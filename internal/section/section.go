// Package section writes GitLab CI's collapsible job-log section markers:
// https://docs.gitlab.com/ee/ci/jobs/job_logs.html#custom-collapsible-sections.
package section

import (
	"fmt"
	"io"
	"time"
)

// Start opens a section. Everything written to w between Start and the
// matching End (same id) is folded under title in the job log — collapsed by
// default unless collapsed is false.
func Start(w io.Writer, id, title string, collapsed bool) {
	opts := ""
	if collapsed {
		opts = "[collapsed=true]"
	}
	fmt.Fprintf(w, "\x1b[0Ksection_start:%d:%s%s\r\x1b[0K\x1b[1;36m%s\x1b[0m\n", time.Now().Unix(), id, opts, title)
}

// End closes the section opened by Start with the same id.
func End(w io.Writer, id string) {
	fmt.Fprintf(w, "\x1b[0Ksection_end:%d:%s\r\x1b[0K\n", time.Now().Unix(), id)
}
