package generator

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"golang.org/x/tools/imports"

	"github.com/mailru/activerecord/internal/pkg/arerror"
	"github.com/mailru/activerecord/internal/pkg/ds"
)

const disclaimer string = `// Code generated by argen. DO NOT EDIT.
// This code was generated from a template.
//
// Manual changes to this file may cause unexpected behavior in your application.
// Manual changes to this file will be overwritten if the code is regenerated.
//
// Generate info: {{ .AppInfo }}
`

type PkgData struct {
	ARPkg            string
	ARPkgTitle       string
	FieldList        []ds.FieldDeclaration
	FieldMap         map[string]int
	FieldObject      map[string]ds.FieldObject
	LinkedObject     map[string]ds.RecordPackage
	ProcInFieldList  []ds.ProcFieldDeclaration
	ProcOutFieldList []ds.ProcFieldDeclaration
	Server           ds.ServerDeclaration
	Container        ds.NamespaceDeclaration
	Indexes          []ds.IndexDeclaration
	Serializers      map[string]ds.SerializerDeclaration
	Mutators         map[string]ds.MutatorDeclaration
	Imports          []ds.ImportDeclaration
	Triggers         map[string]ds.TriggerDeclaration
	Flags            map[string]ds.FlagDeclaration
	AppInfo          string
}

func NewPkgData(appInfo string, cl ds.RecordPackage) PkgData {
	return PkgData{
		ARPkg:            cl.Namespace.PackageName,
		ARPkgTitle:       cl.Namespace.PublicName,
		Indexes:          cl.Indexes,
		FieldList:        cl.Fields,
		FieldMap:         cl.FieldsMap,
		ProcInFieldList:  cl.ProcInFields,
		ProcOutFieldList: cl.ProcOutFields.List(),
		FieldObject:      cl.FieldsObjectMap,
		Server:           cl.Server,
		Container:        cl.Namespace,
		Serializers:      cl.SerializerMap,
		Mutators:         cl.MutatorMap,
		Imports:          cl.Imports,
		Triggers:         cl.TriggerMap,
		Flags:            cl.FlagMap,
		AppInfo:          appInfo,
	}
}

const TemplateName = `ARPkgTemplate`

type GenerateFile struct {
	Data    []byte
	Name    string
	Dir     string
	Backend string
}

type MetaData struct {
	Namespaces []*ds.RecordPackage
	AppInfo    string
}

//nolint:revive
//go:embed tmpl/meta.tmpl
var MetaTmpl string

func GenerateMeta(params MetaData) ([]GenerateFile, *arerror.ErrGeneratorFile) {
	metaWriter := bytes.Buffer{}
	metaFile := bufio.NewWriter(&metaWriter)

	if err := GenerateByTmpl(metaFile, params, "meta", MetaTmpl); err != nil {
		return nil, &arerror.ErrGeneratorFile{Name: "repository.go", Backend: "meta", Filename: "repository.go", Err: err}
	}

	metaFile.Flush()

	genRes := GenerateFile{
		Dir:     "",
		Name:    "repository.go",
		Backend: "meta",
	}

	genData := metaWriter.Bytes()

	var err error

	genRes.Data, err = imports.Process("", genData, nil)
	if err != nil {
		return nil, &arerror.ErrGeneratorFile{Name: "repository.go", Backend: "meta", Filename: genRes.Name, Err: ErrorLine(err, string(genData))}
	}

	return []GenerateFile{genRes}, nil
}

func GenerateByTmpl(dstFile io.Writer, params any, name, tmpl string) *arerror.ErrGeneratorPhases {
	templatePackage, err := template.New(TemplateName).Funcs(funcs).Funcs(OctopusTemplateFuncs).Parse(disclaimer + tmpl)
	if err != nil {
		tmplLines, errgetline := getTmplErrorLine(strings.SplitAfter(disclaimer+tmpl, "\n"), err.Error())
		if errgetline != nil {
			tmplLines = errgetline.Error()
		}

		return &arerror.ErrGeneratorPhases{Backend: name, Phase: "parse", TmplLines: tmplLines, Err: err}
	}

	err = templatePackage.Execute(dstFile, params)
	if err != nil {
		tmplLines, errgetline := getTmplErrorLine(strings.SplitAfter(disclaimer+tmpl, "\n"), err.Error())
		if errgetline != nil {
			tmplLines = errgetline.Error()
		}

		return &arerror.ErrGeneratorPhases{Backend: name, Phase: "execute", TmplLines: tmplLines, Err: err}
	}

	return nil
}

func Generate(appInfo string, cl ds.RecordPackage, linkObject map[string]ds.RecordPackage) (ret []GenerateFile, err error) {
	for _, backend := range cl.Backends {
		var generated map[string]bytes.Buffer

		switch backend {
		case "tarantool15":
			fallthrough
		case "octopus":
			params := NewPkgData(appInfo, cl)
			params.LinkedObject = linkObject

			log.Printf("Generate package (%v)", cl)

			var err *arerror.ErrGeneratorPhases

			generated, err = GenerateOctopus(params)
			if err != nil {
				err.Name = cl.Namespace.PublicName
				return nil, err
			}
		case "tarantool16":
			fallthrough
		case "tarantool2":
			return nil, &arerror.ErrGeneratorFile{Name: cl.Namespace.PublicName, Backend: backend, Err: arerror.ErrGeneratorBackendNotImplemented}
		case "postgres":
			return nil, &arerror.ErrGeneratorFile{Name: cl.Namespace.PublicName, Backend: backend, Err: arerror.ErrGeneratorBackendNotImplemented}
		default:
			return nil, &arerror.ErrGeneratorFile{Name: cl.Namespace.PublicName, Backend: backend, Err: arerror.ErrGeneratorBackendUnknown}
		}

		for name, data := range generated {
			genRes := GenerateFile{
				Dir:     cl.Namespace.PackageName,
				Name:    name + ".go",
				Backend: backend,
			}

			genData := data.Bytes()

			genRes.Data, err = imports.Process("", genData, nil)
			if err != nil {
				return nil, &arerror.ErrGeneratorFile{Name: cl.Namespace.PublicName, Backend: backend, Filename: genRes.Name, Err: ErrorLine(err, string(genData))}
			}

			ret = append(ret, genRes)
		}
	}

	return ret, nil
}

var errImportsRx = regexp.MustCompile(`^(\d+):(\d+):`)

func ErrorLine(errIn error, genData string) error {
	findErr := errImportsRx.FindStringSubmatch(errIn.Error())
	if len(findErr) == 3 {
		lineNum, err := strconv.Atoi(findErr[1])
		if err != nil {
			return errors.Wrap(errIn, "can't unparse error line num")
		}

		lines := strings.Split(genData, "\n")

		if len(lines) < lineNum {
			return errors.Wrap(errIn, fmt.Sprintf("line num %d not found (total %d)", lineNum, len(lines)))
		}

		line := lines[lineNum-1]

		byteNum, err := strconv.Atoi(findErr[2])
		if err != nil {
			return errors.Wrap(errIn, "can't unparse error byte num in line: "+line)
		}

		if len(line) < byteNum {
			return errors.Wrap(errIn, "byte num not found in line: "+line)
		}

		return errors.Wrap(errIn, "\n"+strings.Trim(lines[lineNum-2], "\t")+"\n"+strings.Trim(line, "\t")+"\n"+strings.Repeat(" ", byteNum-1)+"^^^^^"+"\n"+strings.Trim(lines[lineNum], "\t"))
	}

	return errors.Wrap(errIn, "cant parse error message")
}

func GenerateFixture(appInfo string, cl ds.RecordPackage, pkg string, pkgFixture string) ([]GenerateFile, error) {
	var generated map[string]bytes.Buffer

	ret := make([]GenerateFile, 0, 1)

	params := FixturePkgData{
		FixturePkg:       pkgFixture,
		ARPkg:            pkg,
		ARPkgTitle:       cl.Namespace.PublicName,
		FieldList:        cl.Fields,
		FieldMap:         cl.FieldsMap,
		FieldObject:      cl.FieldsObjectMap,
		ProcInFieldList:  cl.ProcInFields,
		ProcOutFieldList: cl.ProcOutFields.List(),
		Container:        cl.Namespace,
		Indexes:          cl.Indexes,
		Serializers:      cl.SerializerMap,
		Imports:          cl.Imports,
		AppInfo:          appInfo,
	}

	log.Printf("Generate package (%v)", cl)

	var err *arerror.ErrGeneratorPhases

	generated, err = generateFixture(params)
	if err != nil {
		err.Name = cl.Namespace.PublicName
		return nil, err
	}

	for _, data := range generated {
		genRes := GenerateFile{
			Dir:  pkgFixture,
			Name: cl.Namespace.PackageName + "_gen.go",
		}

		genData := data.Bytes()

		dataImp, err := imports.Process("", genData, nil)
		if err != nil {
			return nil, &arerror.ErrGeneratorFile{Name: cl.Namespace.PublicName, Backend: "fixture", Filename: genRes.Name, Err: ErrorLine(err, string(genData))}
		}

		genRes.Data = dataImp
		ret = append(ret, genRes)
	}

	return ret, nil
}
