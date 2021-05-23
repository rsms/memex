package supervisor_test

import (
	"time"

	"github.com/rsms/memex/extension"
)

func init() {
	extension.Register("supervisor-test", run)
}

func run(ext extension.Extension) {
	log := ext.Logger()
	log.Info("starting")

	// // panic right away
	// panic("meow")

	// // panic during shutdown
	// <-ext.Done()
	// panic("meow")

	// properly wait
	<-ext.Done()

	// // wait forever (times out)
	// ch := make(chan bool)
	// <-ch

	// simulate doing some slow shutdown work
	for i := 0; i < 3; i++ {
		log.Info("simulating slow shutdown (%d/3)", i+1)
		time.Sleep(200 * time.Millisecond)
	}
}
