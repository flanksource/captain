package monitor

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("macOS transcript descriptors", func() {
	It("closes the watched transcript descriptor after every session ends", func() {
		project := GinkgoT().TempDir()
		path := writeTranscript(project, "session.jsonl")
		m := &Monitor{cfg: Config{Debounce: time.Hour}, tracked: map[string]string{}}
		w, err := newTranscriptWatcher(m, newIngestor(m))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.close)
		openPaths := func() []string {
			GinkgoHelper()
			out, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(os.Getpid()), "-Fn").Output()
			Expect(err).NotTo(HaveOccurred())
			return strings.Split(string(out), "\n")
		}
		for range 3 {
			m.TrackTranscript(path, "claude")
			w.track(path, "claude")
			Expect(openPaths()).To(ContainElement("n" + path))
			m.untrackTranscript(path)
			Expect(openPaths()).NotTo(ContainElement("n" + path))
		}
	})
})
