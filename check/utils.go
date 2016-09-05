package check

import (
	"bufio"
	"bytes"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var (
	skipDirs       = []string{"Godeps", "vendor", "third_party"}
	skipSuffixes   = []string{".pb.go", ".pb.gw.go", ".generated.go", "bindata.go", "_string.go"}
	skipFirstLines = []string{"code generated", "generated", "autogenerated", "@generated", "code autogenerated", "auto-generated"}
)

func addSkipDirs(params []string) []string {
	for _, dir := range skipDirs {
		params = append(params, fmt.Sprintf("--skip=%s", dir))
	}
	return params
}

// GoFiles returns a slice of Go filenames
// in a given directory.
func GoFiles(dir string) (filenames, skipped []string, err error) {
	visit := func(fp string, fi os.FileInfo, err error) error {
		for _, skip := range skipDirs {
			if strings.Contains(fp, fmt.Sprintf("/%s/", skip)) {
				return nil
			}
		}
		if err != nil {
			fmt.Println(err) // can't walk here,
			return nil       // but continue walking elsewhere
		}
		if fi.IsDir() {
			return nil // not a file.  ignore.
		}
		fiName := fi.Name()
		for _, skip := range skipSuffixes {
			if strings.HasSuffix(fiName, skip) {
				skipped = append(skipped, fp)
				return nil
			}
		}
		ext := filepath.Ext(fiName)
		if ext != ".go" {
			return nil
		}

		if autoGenerated(fp) {
			skipped = append(skipped, fp)
			return nil
		}

		filenames = append(filenames, fp)

		return nil
	}

	err = filepath.Walk(dir, visit)

	return filenames, skipped, err
}

// RenameFiles renames the provided filenames to have a ".grc.bk" extension,
// so they will not be considered in future checks.
func RenameFiles(names []string) (err error) {
	for i := range names {
		tmpErr := os.Rename(names[i], names[i]+".grc.bk")
		if tmpErr != nil {
			// save this error, but still continue with other files
			err = tmpErr
		}
	}

	return err
}

// RevertFiles removes the ".grc.bk" extension from files
func RevertFiles(names []string) (err error) {
	for i := range names {
		tmpErr := os.Rename(names[i]+".grc.bk", names[i])
		if tmpErr != nil {
			// save this error, but still continue with other files
			err = tmpErr
		}
	}

	return err
}

// lineCount returns the number of lines in a given file
func lineCount(filepath string) (int, error) {
	out, err := exec.Command("wc", "-l", filepath).Output()
	if err != nil {
		return 0, err
	}
	// wc output is like: 999 filename.go
	count, err := strconv.Atoi(strings.Split(strings.TrimSpace(string(out)), " ")[0])
	if err != nil {
		return 0, err
	}

	return count, nil
}

// determine whether the Go file was auto-generated
func autoGenerated(fp string) bool {
	file, err := os.Open(fp)
	if err != nil {
		fmt.Println(err)
		return false
	}
	defer file.Close()

	// read first line of file and determine if it might
	// be auto-generated
	scanner := bufio.NewScanner(file)
	scanner.Scan()
	line := strings.ToLower(scanner.Text())
	commentStyles := []string{"// ", "//", "/* ", "/*"}
	for _, skip := range skipFirstLines {
		for i := range commentStyles {
			if strings.HasPrefix(line, commentStyles[i]) && strings.HasPrefix(line[len(commentStyles[i]):], skip) {
				return true
			}
		}
	}
	return false
}

// Error contains the line number and the reason for
// an error output from a command
type Error struct {
	LineNumber  int    `json:"line_number"`
	ErrorString string `json:"error_string"`
}

// FileSummary contains the filename, location of the file
// on GitHub, and all of the errors related to the file
type FileSummary struct {
	Filename string  `json:"filename"`
	FileURL  string  `json:"file_url"`
	Errors   []Error `json:"errors"`
}

// AddError adds an Error to FileSummary
func (fs *FileSummary) AddError(out string) error {
	s := strings.SplitN(out, ":", 2)
	msg := strings.SplitAfterN(s[1], ":", 3)[2]

	e := Error{ErrorString: msg}
	ls := strings.Split(s[1], ":")
	ln, err := strconv.Atoi(ls[0])
	if err != nil {
		return err
	}
	e.LineNumber = ln

	fs.Errors = append(fs.Errors, e)

	return nil
}

func fileURL(dir, filename string) string {
	var fileURL string
	base := strings.TrimPrefix(dir, "repos/src/")
	switch {
	case strings.HasPrefix(base, "golang.org/x/"):
		var pkg string
		if len(strings.Split(base, "/")) >= 3 {
			pkg = strings.Split(base, "/")[2]
		}
		return fmt.Sprintf("https://github.com/golang/%s/blob/master%s", pkg, strings.TrimPrefix(filename, "/"+base))
	case strings.HasPrefix(base, "github.com/"):
		if len(strings.Split(base, "/")) == 4 {
			base = strings.Join(strings.Split(base, "/")[0:3], "/")
		}
		return fmt.Sprintf("https://%s/blob/master%s", base, strings.TrimPrefix(filename, "/"+base))
	}

	return fileURL
}

// GoTool runs a given go command (for example gofmt, go tool vet)
// on a directory
func GoTool(dir string, filenames, command []string) (float64, []FileSummary, error) {
	params := command[1:]
	params = addSkipDirs(params)
	params = append(params, dir+"/...")

	cmd := exec.Command(command[0], params...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, []FileSummary{}, err
	}

	err = cmd.Start()
	if err != nil {
		return 0, []FileSummary{}, err
	}

	out := bufio.NewScanner(stdout)

	// the same file can appear multiple times out of order
	// in the output, so we can't go line by line, have to store
	// a map of filename to FileSummary
	fsMap := map[string]FileSummary{}
	var failed = []FileSummary{}
outer:
	for out.Scan() {
		filename := strings.Split(out.Text(), ":")[0]
		filename = strings.TrimPrefix(filename, "repos/src")
		for _, skip := range skipSuffixes {
			if strings.HasSuffix(filename, skip) {
				continue outer
			}
		}

		if autoGenerated("repos/src" + filename) {
			continue outer
		}

		fu := fileURL(dir, filename)
		fs := fsMap[filename]
		if fs.Filename == "" {
			fs.Filename = filename
			if strings.HasPrefix(filename, "/github.com") {
				sp := strings.Split(filename, "/")
				if len(sp) > 3 {
					fs.Filename = strings.Join(sp[3:], "/")
				}

			}
			fs.FileURL = fu
		}
		err = fs.AddError(out.Text())
		if err != nil {
			return 0, []FileSummary{}, err
		}
		fsMap[filename] = fs
	}
	if err := out.Err(); err != nil {
		return 0, []FileSummary{}, err
	}

	for _, v := range fsMap {
		failed = append(failed, v)
	}

	err = cmd.Wait()
	if exitErr, ok := err.(*exec.ExitError); ok {
		// The program has exited with an exit code != 0

		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			// some commands exit 1 when files fail to pass (for example go vet)
			if status.ExitStatus() != 1 {
				return 0, failed, err
				// return 0, Error{}, err
			}
		}
	}

	if len(filenames) == 1 {
		lc, err := lineCount(filenames[0])
		if err != nil {
			return 0, failed, err
		}

		var errors int
		if len(failed) != 0 {
			errors = len(failed[0].Errors)
		}

		return float64(lc-errors) / float64(lc), failed, nil
	}

	return float64(len(filenames)-len(failed)) / float64(len(filenames)), failed, nil
}

// GoFmtNative runs gofmt via golang's stdlib format pkg
func GoFmtNative(dir string, filenames []string) (float64, []FileSummary, error) {
	var failed = []FileSummary{}
	for _, f := range filenames {
		for _, skip := range skipSuffixes {
			if strings.HasSuffix(f, skip) {
				continue
			}
		}

		if autoGenerated(f) {
			continue
		}

		b, err := ioutil.ReadFile(f)
		if err != nil {
			return 0, []FileSummary{}, err
		}
		g, err := format.Source(b)
		if err != nil {
			return 0, []FileSummary{}, err
		}
		if !bytes.Equal(b, g) {
			filename := strings.TrimPrefix(f, "repos/src")
			fs := FileSummary{}
			fs.Filename = filename
			if strings.HasPrefix(filename, "/github.com") {
				sp := strings.Split(filename, "/")
				if len(sp) > 3 {
					fs.Filename = strings.Join(sp[3:], "/")
				}
			}
			fu := fileURL(dir, strings.TrimPrefix(f, "repos/src"))
			fs.FileURL = fu
			fs.Errors = append(fs.Errors, Error{1, "file is not gofmted"})
			failed = append(failed, fs)
		}
	}

	return float64(len(filenames)-len(failed)) / float64(len(filenames)), failed, nil
}
