package profile

import (
	"os"
	"runtime"
	"runtime/pprof"

	log "github.com/sirupsen/logrus"
)

func StartMemProfile (filename string) {
	runtime.GC()
	f, err := os.Create(filename)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "filename": filename}).Error("Create mem profile file failed")
	}
	log.WithField("Create file name", filename).Info("Create mem profile file")
	pprof.Lookup("heap").WriteTo(f, 1)
	defer f.Close()
}


