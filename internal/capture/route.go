package capture

import (
	"os"
	"os/exec"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func pactlOut(args ...string) string {
	cmd := exec.Command("pactl", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	s := strings.Join(strings.Fields(string(out)), " ")
	if err != nil {
		logx.Debugf("capture", "pactlOut: pactl %v: %v", args, err)
		if s == "" {
			s = err.Error()
		} else {
			s = "err=" + err.Error() + " out=" + s
		}
	}
	if s == "" {
		s = "-"
	}
	return s
}

// LogRoute prints where PulseAudio puts streams, so it is possible to see
// whether the recording Chromium actually feeds the ssau_rec monitor.
func LogRoute(component, stage string) {
	logx.Infof(component, "audio route %s: xdg=%q pulse_sink=%q pulse_source=%q default_sink=%q sinks=[%s] inputs=[%s]",
		stage,
		os.Getenv("XDG_RUNTIME_DIR"),
		os.Getenv("PULSE_SINK"),
		os.Getenv("PULSE_SOURCE"),
		pactlOut("get-default-sink"),
		pactlOut("list", "short", "sinks"),
		pactlOut("list", "short", "sink-inputs"),
	)
}
