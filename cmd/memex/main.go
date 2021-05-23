package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rsms/go-log"
	"github.com/rsms/memex/extension"
	// _ "github.com/rsms/memex/extension/webbrowser"
	// _ "github.com/rsms/memex/extension/test"
)

var MEMEX_VERSION string = "0.0.0" // set at compile time
var MEMEX_BUILDTAG string = "src"  // set at compile time
var MEMEX_ROOT string              // root file directory for the memex program & its files
var MEMEX_DIR string               // root file directory for the memex repo

// program-wide index database
var indexdb *IndexDB

func main() {
	defer log.Sync()
	var err error

	// parse CLI flags
	optMemexDir := flag.String("D", "", "Set MEMEX_DIR (overrides environment var)")
	optVersion := flag.Bool("version", false, "Print version and exit")
	optDebug := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	// want version?
	if *optVersion {
		fmt.Printf("memex version %s build %s\n", MEMEX_VERSION, MEMEX_BUILDTAG)
		os.Exit(0)
	}

	// update log level based on CLI options
	if *optDebug {
		log.RootLogger.Level = log.LevelDebug
		log.RootLogger.EnableFeatures(log.FSync)
	} else {
		log.RootLogger.EnableFeatures(log.FSyncError)
	}

	// log version
	log.Info("memex v%s+%s\n", MEMEX_VERSION, MEMEX_BUILDTAG)

	// MEMEX_ROOT = dirname(dirname(argv[0])) (since {MEMEX_ROOT}/bin/memex)
	MEMEX_ROOT = filepath.Dir(filepath.Dir(mustRealPath(os.Args[0])))

	// MEMEX_DIR
	MEMEX_DIR = *optMemexDir
	crashOnErr(init_MEMEX_DIR())
	log.Debug("MEMEX_ROOT=%q", MEMEX_ROOT)
	log.Debug("MEMEX_DIR=%q", MEMEX_DIR)

	// update env
	os.Setenv("MEMEX_ROOT", MEMEX_ROOT)
	os.Setenv("MEMEX_DIR", MEMEX_DIR)
	os.Setenv("MEMEX_VERSION", MEMEX_VERSION)

	// open index database
	indexdb, err = IndexDBOpen(
		filepath.Join(MEMEX_DIR, ".memex-index"),
		log.SubLogger("[indexdb]"))
	crashOnErr(err)
	RegisterExitHandler(indexdb.Close)

	// change working directory to MEMEX_DIR
	crashOnErr(os.Chdir(MEMEX_DIR))

	// start extension supervisor
	if len(extension.Registry) > 0 {
		extSupervisor := NewExtSupervisor(log.SubLogger("[extsv]"))
		RegisterExitHandler(extSupervisor.Shutdown)
		if err := extSupervisor.Start(context.Background(), extension.Registry); err != nil {
			log.Error("extension supervisor: %v", err)
		}
	}

	// start service supervisor
	srvSupervisor := NewServiceSupervisor(log.RootLogger, MEMEX_DIR)
	RegisterExitHandler(srvSupervisor.Shutdown)
	if err := srvSupervisor.Start(context.Background()); err != nil {
		log.Error("service supervisor: %v", err)
	}

	// channel closes when all exit handlers have completed
	<-ExitCh
}

// init_MEMEX_DIR initializes MEMEX_DIR
func init_MEMEX_DIR() error {
	var err error
	if MEMEX_DIR == "" {
		MEMEX_DIR = os.Getenv("MEMEX_DIR")
		if MEMEX_DIR == "" {
			return errorf("MEMEX_DIR not set in environment nor passed as a CLI option")
		}
	}
	// realpath
	MEMEX_DIR, err = filepath.Abs(MEMEX_DIR)
	if err != nil {
		return err
	}
	// make sure it's a directory
	if err = isdir(MEMEX_DIR); err != nil {
		return errorf("MEMEX_DIR: %v", err)
	}
	// SERVICE_DIR = filepath.Join(MEMEX_DIR, "services")
	// if err = os.MkdirAll(SERVICE_DIR, 0700); err != nil {
	// 	return err
	// }
	return nil
}

func mustRealPath(path string) string {
	abspath, err := filepath.Abs(path)
	if err == nil {
		abspath, err = filepath.EvalSymlinks(abspath)
	}
	if err != nil {
		panic(err)
	}
	return abspath
}

func crashOnErr(err error) {
	if err != nil {
		fatalf(err)
	}
}
