package ops

import (
	"fmt"
	"io"
	"time"
)

// Logger prints timestamped lines, so cron/launchd output can be read back later.
type Logger struct {
	Out, Err io.Writer
	Tag      string
}

func (l Logger) line(w io.Writer, mark, format string, a ...any) {
	fmt.Fprintf(w, "[%s] [%s] %s%s\n", time.Now().UTC().Format(time.RFC3339), l.Tag, mark, fmt.Sprintf(format, a...))
}

func (l Logger) Info(format string, a ...any)  { l.line(l.Out, "", format, a...) }
func (l Logger) Warn(format string, a ...any)  { l.line(l.Err, "warning: ", format, a...) }
func (l Logger) Error(format string, a ...any) { l.line(l.Err, "error: ", format, a...) }
