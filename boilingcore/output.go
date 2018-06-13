package boilingcore

import (
	"bufio"
	"bytes"
	"fmt"
	"go/format"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strconv"
	"text/template"

	"github.com/pkg/errors"
	"github.com/volatiletech/sqlboiler/importers"
)

var noEditDisclaimer = []byte(`// Code generated by SQLBoiler (https://github.com/volatiletech/sqlboiler). DO NOT EDIT.
// This file is meant to be re-generated in place and/or deleted at any time.

`)

var (
	// templateByteBuffer is re-used by all template construction to avoid
	// allocating more memory than is needed. This will later be a problem for
	// concurrency, address it then.
	templateByteBuffer = &bytes.Buffer{}

	rgxRemoveNumberedPrefix = regexp.MustCompile(`^[0-9]+_`)
	rgxSyntaxError          = regexp.MustCompile(`(\d+):\d+: `)

	testHarnessWriteFile = ioutil.WriteFile
)

// generateOutput builds the file output and sends it to outHandler for saving
func generateOutput(state *State, data *templateData) error {
	return executeTemplates(executeTemplateData{
		state:                state,
		data:                 data,
		templates:            state.Templates,
		importSet:            state.Config.Imports.All,
		combineImportsOnType: true,
		fileSuffix:           ".go",
	})
}

// generateTestOutput builds the test file output and sends it to outHandler for saving
func generateTestOutput(state *State, data *templateData) error {
	return executeTemplates(executeTemplateData{
		state:                state,
		data:                 data,
		templates:            state.TestTemplates,
		importSet:            state.Config.Imports.Test,
		combineImportsOnType: false,
		fileSuffix:           "_test.go",
	})
}

// generateSingletonOutput processes the templates that should only be run
// one time.
func generateSingletonOutput(state *State, data *templateData) error {
	return executeSingletonTemplates(executeTemplateData{
		state:          state,
		data:           data,
		templates:      state.SingletonTemplates,
		importNamedSet: state.Config.Imports.Singleton,
		fileSuffix:     ".go",
	})
}

// generateSingletonTestOutput processes the templates that should only be run
// one time.
func generateSingletonTestOutput(state *State, data *templateData) error {
	return executeSingletonTemplates(executeTemplateData{
		state:          state,
		data:           data,
		templates:      state.SingletonTestTemplates,
		importNamedSet: state.Config.Imports.TestSingleton,
		fileSuffix:     ".go",
	})
}

type executeTemplateData struct {
	state *State
	data  *templateData

	templates *templateList

	importSet      importers.Set
	importNamedSet importers.Map

	combineImportsOnType bool

	fileSuffix string
}

func executeTemplates(e executeTemplateData) error {
	if e.data.Table.IsJoinTable {
		return nil
	}

	out := templateByteBuffer
	out.Reset()

	var imps importers.Set
	imps.Standard = e.importSet.Standard
	imps.ThirdParty = e.importSet.ThirdParty
	if e.combineImportsOnType {
		colTypes := make([]string, len(e.data.Table.Columns))
		for i, ct := range e.data.Table.Columns {
			colTypes[i] = ct.Type
		}

		imps = importers.AddTypeImports(imps, e.state.Config.Imports.BasedOnType, colTypes)
	}

	writeFileDisclaimer(out)
	writePackageName(out, e.state.Config.PkgName)
	writeImports(out, imps)

	for _, tplName := range e.templates.Templates() {
		if err := executeTemplate(out, e.templates.Template, tplName, e.data); err != nil {
			return err
		}
	}

	fName := e.data.Table.Name + e.fileSuffix
	if err := writeFile(e.state.Config.OutFolder, fName, out); err != nil {
		return err
	}

	return nil
}

func executeSingletonTemplates(e executeTemplateData) error {
	if e.data.Table.IsJoinTable {
		return nil
	}

	out := templateByteBuffer
	for _, tplName := range e.templates.Templates() {
		out.Reset()

		fName := tplName
		ext := filepath.Ext(fName)
		fName = rgxRemoveNumberedPrefix.ReplaceAllString(fName[:len(fName)-len(ext)], "")

		imps := importers.Set{
			Standard:   e.importNamedSet[fName].Standard,
			ThirdParty: e.importNamedSet[fName].ThirdParty,
		}

		writeFileDisclaimer(out)
		writePackageName(out, e.state.Config.PkgName)
		writeImports(out, imps)

		if err := executeTemplate(out, e.templates.Template, tplName, e.data); err != nil {
			return err
		}

		if err := writeFile(e.state.Config.OutFolder, fName+e.fileSuffix, out); err != nil {
			return err
		}
	}

	return nil
}

// writeFileDisclaimer writes the disclaimer at the top with a trailing
// newline so the package name doesn't get attached to it.
func writeFileDisclaimer(out *bytes.Buffer) {
	_, _ = out.Write(noEditDisclaimer)
}

// writePackageName writes the package name correctly, ignores errors
// since it's to the concrete buffer type which produces none
func writePackageName(out *bytes.Buffer, pkgName string) {
	_, _ = fmt.Fprintf(out, "package %s\n\n", pkgName)
}

// writeImports writes the package imports correctly, ignores errors
// since it's to the concrete buffer type which produces none
func writeImports(out *bytes.Buffer, imps importers.Set) {
	if impStr := imps.Format(); len(impStr) > 0 {
		_, _ = fmt.Fprintf(out, "%s\n", impStr)
	}
}

// writeFile writes to the given folder and filename, formatting the buffer
// given.
func writeFile(outFolder string, fileName string, input *bytes.Buffer) error {
	byt, err := formatBuffer(input)
	if err != nil {
		return err
	}

	path := filepath.Join(outFolder, fileName)
	if err = testHarnessWriteFile(path, byt, 0666); err != nil {
		return errors.Wrapf(err, "failed to write output file %s", path)
	}

	return nil
}

// executeTemplate takes a template and returns the output of the template
// execution.
func executeTemplate(buf *bytes.Buffer, t *template.Template, name string, data *templateData) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Errorf("failed to execute template: %s\npanic: %+v\n", name, r)
		}
	}()

	if err := t.ExecuteTemplate(buf, name, data); err != nil {
		return errors.Wrapf(err, "failed to execute template: %s", name)
	}
	return nil
}

func formatBuffer(buf *bytes.Buffer) ([]byte, error) {
	output, err := format.Source(buf.Bytes())
	if err == nil {
		return output, nil
	}

	matches := rgxSyntaxError.FindStringSubmatch(err.Error())
	if matches == nil {
		return nil, errors.Wrap(err, "failed to format template")
	}

	lineNum, _ := strconv.Atoi(matches[1])
	scanner := bufio.NewScanner(buf)
	errBuf := &bytes.Buffer{}
	line := 1
	for ; scanner.Scan(); line++ {
		if delta := line - lineNum; delta < -5 || delta > 5 {
			continue
		}

		if line == lineNum {
			errBuf.WriteString(">>>> ")
		} else {
			fmt.Fprintf(errBuf, "% 4d ", line)
		}
		errBuf.Write(scanner.Bytes())
		errBuf.WriteByte('\n')
	}

	return nil, errors.Wrapf(err, "failed to format template\n\n%s\n", errBuf.Bytes())
}
