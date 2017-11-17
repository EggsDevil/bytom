package profile

import (
	"os"
	"runtime/pprof"

	log "github.com/sirupsen/logrus"
)

func StartCpuProfile(filename string) {
	f, err := os.Create(filename)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "filename": filename}).Error("Create cpu profile file failed")
	}
	log.WithField("Create file name", filename).Info("Create cpu profile file")
	pprof.StartCPUProfile(f)
}

func StopCpuProfile() {
	pprof.StopCPUProfile()
}


