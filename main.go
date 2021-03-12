package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/c2h5oh/datasize"
	"github.com/nickalie/go-webpbin"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

func printLogo() {
	fmt.Print(`
                                 _            
                                | |           
  ____    ___  __      __  ___  | |__    _ __  
 / _  |  / _ \ \ \ /\ / / / _ \ | '_ \  | '_ \
| (_| | | (_) | \ V  V / |  __/ | |_) | | |_) |
\___, |  \___/   \_/\_/   \___| |_.__/  | .__/
 ___/ |                                 | |
|____/                                  |_|

`)
}

var (
	imageRe          = regexp.MustCompile(`(?i)\.(jpe?g|png)$`)
	quality          uint
	dir              string
	replace          bool
	workers          int
	dryRun           bool
	prependToName    string
	appendToName     string
	inputMinFileSize string
	minFileSize      datasize.ByteSize
)

// set the flags
func init() {
	// do not download binary
	webpbin.SkipDownload()
	flag.StringVar(&dir, "d", "", "the directory to crawl")
	flag.UintVar(&quality, "q", 0, "the quality for the webp images")
	flag.BoolVar(&replace, "r", false, "replace existing webp files")
	flag.StringVar(&prependToName, "prepend", "", "prepend string to the beginning of file name")
	flag.StringVar(&appendToName, "append", "", "append string to the end of file name")
	flag.StringVar(&inputMinFileSize, "min-size", "10KB",
		"smallest file size that will have a webp image created")
	flag.BoolVar(&dryRun, "dry-run", false, "whether to handle this as a dry run and only " +
		"print target files")
	flag.IntVar(&workers, "w", runtime.NumCPU(), "the number of worker routines to spawn. " +
		"Defaults to number of CPUs.")

	flag.Parse()

	err := minFileSize.UnmarshalText([]byte(inputMinFileSize))

	if err != nil {
		log.Printf("!!ERROR: %s is not a valid file size", inputMinFileSize)
		os.Exit(1)
	}

	// log to standard output
	log.SetOutput(os.Stdout)
}

func mustGetFileSize(file string) int64 {
	fi, err := os.Stat(file)
	if err != nil {
		panic(err)
	}
	return fi.Size()
}

type webpJobResult struct {
	err         error
	compression float64
	exists      bool
	outputFile  string
}

func newJob(input string, quality uint) *job {
	j := &job{
		input:   input,
		quality: quality,
		resCh:   make(chan *webpJobResult),
	}
	return j
}

type job struct {
	input   string
	quality uint
	res     *webpJobResult
	resCh   chan *webpJobResult
}

// waitForResult gets a result for this job only when job completion signal is set
func (j *job) waitForResult() *webpJobResult {
	j.res = <-j.resCh
	return j.res
}

func newPool(ctx context.Context, workers int) *pool {
	ctx, done := context.WithCancel(ctx)
	p := &pool{
		workers: workers,
		jobs:    make(chan *job),
		ctx:     ctx,
		done:    done,
		wg:      &sync.WaitGroup{},
	}
	p.start()
	return p
}

type pool struct {
	workers int
	jobs    chan *job
	ctx     context.Context
	done    context.CancelFunc
	wg      *sync.WaitGroup
}

// execute executes a compression job
func (p *pool) execute(j *job) {
	go j.waitForResult()
	r := &webpJobResult{}

	// always pass the result to the job's result channel
	defer func() {
		j.resCh <- r
		close(j.resCh)
	}()

	var (
		targetExt string
	)

	// get the absolute path
	j.input, r.err = filepath.Abs(j.input)
	if r.err != nil {
		return
	}

	// get the target's extension
	targetExt = filepath.Ext(j.input)

	base := filepath.Base(j.input)
	path := filepath.Dir(j.input)

	// output is the old filepath with new webp extension and prepend and append strings
	r.outputFile = filepath.Join(path, prependToName + base[:len(base)-len(targetExt)] + appendToName + ".webp")

	// check if file already exists
	if !replace {
		if _, err := os.Stat(r.outputFile); err == nil {
			// file already exists
			r.exists = true
			log.Println(j.input, "already has a webp version")
			return
		}
	}

	// get the size of the original file
	fSizeTarget := datasize.ByteSize(mustGetFileSize(j.input))

	if fSizeTarget.Bytes() < minFileSize.Bytes() {
		// nothing to do
		log.Printf("%s size [%s] is smaller than the minimum file size [%s]. Skipping...",
			j.input, fSizeTarget.HumanReadable(), minFileSize.HumanReadable())
		return
	}

	if dryRun {
		// if it's a dry run then just print and return
		log.Printf("%s \u2192 %s [?]\n", j.input, r.outputFile)
		return
	}

	r.err = webpbin.NewCWebP().
		Quality(j.quality).
		InputFile(j.input).
		OutputFile(r.outputFile).
		Run()

	if r.err != nil {
		return
	}

	// get the file size of the new file
	fSizeOutput := datasize.ByteSize(mustGetFileSize(r.outputFile))

	// calculate the compression percentage
	r.compression = (1 - (float64(fSizeOutput) / float64(fSizeTarget))) * 100

	if r.err != nil {
		log.Printf("!ERROR webp generation for %s FAILED with error: %s\n", r.err)
	} else {
		if fSizeOutput.Bytes() > fSizeTarget.Bytes() {
			// webp is bigger than output file???
			log.Printf("!WARNING output file %s is bigger than input file %s. deleting...", r.outputFile, j.input)
			r.err = os.Remove(r.outputFile)
			if r.err != nil {
				// should never happen this error but return the error if we have one
				return
			}

		}
		log.Printf("%s (%s) \u2192 %s (%s) [%.2f%%]\n",
			j.input, fSizeTarget.HumanReadable(), r.outputFile, fSizeOutput.HumanReadable(), r.compression)
	}

	return
}

func (p *pool) start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

func (p *pool) wait() {
	close(p.jobs)
	p.wg.Wait()
}

func (p *pool) stop() {
	p.done()
	p.wg.Wait()
}

func (p *pool) worker() {
	defer func() {
		p.wg.Done()
	}()
	for {
		select {
		case j, ok := <-p.jobs:
			if !ok {
				// no more work
				return
			}
			// execute a job and pass the result into the result channel
			p.execute(j)
		case <-p.ctx.Done():
			// we'imageRe done early
			return
		}
	}
}

func main() {
	printLogo()
	if (len(dir) < 1 || quality < 1) && !dryRun {
		// print help
		fmt.Print(`
gowebp is a tool used to create webp images from jpegs and png files

Usage:
`)
		flag.PrintDefaults()
		os.Exit(1)
	}

	p := newPool(context.Background(), workers)

	dir = strings.TrimSpace(dir)

	dir, err := filepath.Abs(dir)

	if err != nil {
		fmt.Println("dir is not valid!")
		os.Exit(2)
	}

	fmt.Println("CRAWLING:\t", dir)
	fmt.Println("QUALITY:\t", quality)
	fmt.Println("WORKERS:\t", workers)
	fmt.Println("MIN FILE SIZE:\t", minFileSize.String())
	if dryRun {
		fmt.Println("*** THIS IS A DRY RUN ***")
	}

	// stop pool when exiting
	defer p.stop()

	cnt := 0
	err = filepath.Walk(dir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if imageRe.MatchString(info.Name()) {
				//log.Println("found image:", path)
				p.jobs <- newJob(path, quality)
				cnt += 1
			}

			return nil
		})
	if err != nil {
		log.Println("!!ERROR", err)
	}

	p.wait()
}
