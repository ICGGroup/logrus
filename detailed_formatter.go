package logrus

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// FunctionKey holds the function field
const FunctionKey = "function"

// PackageKey holds the package field
const PackageKey = "package"

// LineKey holds the line field
const LineKey = "line"

// FileKey holds the file field
const FileKey = "file"

const (
	logrusStackJump          = 4
	logrusFieldlessStackJump = 6
)

// DetailedFormatter decorates log entries with function name and package name (optional) and line number (optional)
type DetailedFormatter struct {
	ChildFormatter Formatter
	// When true, line number will be tagged to fields as well
	Line bool
	// When true, package name will be tagged to fields as well
	Package bool
	// When true, file name will be tagged to fields as well
	File bool
	// When true, function name will be tagged to fields as well
	Function bool
	// When true, only base name of the file will be tagged to fields
	BaseNameOnly bool
}

// Format the current log entry by adding the function name and line number of the caller.
func (f *DetailedFormatter) Format(entry *Entry) ([]byte, error) {
	function, file, line := f.getCurrentPosition(entry)

	packageEnd := strings.LastIndex(function, ".")
	functionName := function[packageEnd+1:]

	data := Fields{}
	if f.Function {
		data[FunctionKey] = functionName
	}
	if f.Line {
		data[LineKey] = line
	}
	if f.Package {
		packageName := function[:packageEnd]
		data[PackageKey] = packageName
	}
	if f.File {
		if f.BaseNameOnly {
			data[FileKey] = filepath.Base(file)
		} else {
			data[FileKey] = file
		}
	}
	for k, v := range entry.Data {
		data[k] = v
	}
	entry.Data = data

	return f.ChildFormatter.Format(entry)
}

func (f *DetailedFormatter) getCurrentPosition(entry *Entry) (string, string, string) {
	skip := logrusStackJump
	if len(entry.Data) == 0 {
		skip = logrusFieldlessStackJump
	}
start:
	pc, file, line, _ := runtime.Caller(skip)
	lineNumber := ""
	if f.Line {
		lineNumber = fmt.Sprintf("%d", line)
	}
	function := runtime.FuncForPC(pc).Name()
	if strings.LastIndex(function, "icggroup/logrus.") != -1 {
		skip++
		goto start
	}
	return function, file, lineNumber
}
