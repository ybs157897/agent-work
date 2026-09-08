// Package projectmodel builds a lightweight IDEA-like project snapshot:
// Maven modules from pom.xml, declared dependencies, and host JDK.
package projectmodel

import (
	"bytes"
	"encoding/xml"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type BuildSystem string

const (
	BuildMaven  BuildSystem = "maven"
	BuildGradle BuildSystem = "gradle"
	BuildNone   BuildSystem = "none"
)

type JDKInfo struct {
	Home    string `json:"home,omitempty"`
	Version string `json:"version,omitempty"`
	Vendor  string `json:"vendor,omitempty"`
}

type ModuleInfo struct {
	Path       string `json:"path"` // workspace-relative; "" = root module
	ArtifactID string `json:"artifact_id,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	Version    string `json:"version,omitempty"`
	Packaging  string `json:"packaging,omitempty"`
	Name       string `json:"name,omitempty"`
}

type DependencyInfo struct {
	GroupID    string `json:"group_id"`
	ArtifactID string `json:"artifact_id"`
	Version    string `json:"version,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Coord      string `json:"coord"`
}

type ProjectInfo struct {
	Build           BuildSystem      `json:"build"`
	JDK             JDKInfo          `json:"jdk"`
	LanguageLevel   string           `json:"language_level,omitempty"`
	RootModule      *ModuleInfo      `json:"root_module,omitempty"`
	Modules         []ModuleInfo     `json:"modules,omitempty"`
	Dependencies    []DependencyInfo `json:"dependencies,omitempty"`
	DependencyTrunc bool             `json:"dependencies_truncated,omitempty"`
}

type pomXML struct {
	ArtifactID string `xml:"artifactId"`
	GroupID    string `xml:"groupId"`
	Version    string `xml:"version"`
	Packaging  string `xml:"packaging"`
	Name       string `xml:"name"`
	Parent     struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	} `xml:"parent"`
	Modules    []string `xml:"modules>module"`
	Properties struct {
		JavaVersion          string `xml:"java.version"`
		MavenCompilerSource  string `xml:"maven.compiler.source"`
		MavenCompilerRelease string `xml:"maven.compiler.release"`
	} `xml:"properties"`
	Dependencies []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
		Scope      string `xml:"scope"`
	} `xml:"dependencies>dependency"`
}

var xmlnsAttr = regexp.MustCompile(`\sxmlns(:\w+)?="[^"]*"`)

func stripXMLNS(data []byte) []byte {
	return xmlnsAttr.ReplaceAll(data, nil)
}

func readPom(abs string) (*pomXML, error) {
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var pom pomXML
	if err := xml.Unmarshal(stripXMLNS(raw), &pom); err != nil {
		return nil, err
	}
	return &pom, nil
}

func (p *pomXML) effectiveGroup() string {
	if p.GroupID != "" {
		return p.GroupID
	}
	return p.Parent.GroupID
}

func (p *pomXML) effectiveVersion() string {
	if p.Version != "" {
		return p.Version
	}
	return p.Parent.Version
}

func (p *pomXML) languageLevel() string {
	if p.Properties.JavaVersion != "" {
		return p.Properties.JavaVersion
	}
	if p.Properties.MavenCompilerRelease != "" {
		return p.Properties.MavenCompilerRelease
	}
	return p.Properties.MavenCompilerSource
}

const maxDeps = 80
const maxModuleDepth = 4

// Inspect walks the workspace root for Maven/Gradle markers and returns a project snapshot.
func Inspect(root string) ProjectInfo {
	info := ProjectInfo{
		Build: BuildNone,
		JDK:   detectJDK(),
	}
	pomPath := filepath.Join(root, "pom.xml")
	if _, err := os.Stat(pomPath); err == nil {
		info.Build = BuildMaven
		fillMaven(&info, root, "", 0)
		return info
	}
	for _, name := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			info.Build = BuildGradle
			base := filepath.Base(root)
			info.RootModule = &ModuleInfo{Path: "", ArtifactID: base, Name: base}
			return info
		}
	}
	return info
}

func fillMaven(info *ProjectInfo, root, rel string, depth int) {
	pomFile := "pom.xml"
	if rel != "" {
		pomFile = filepath.Join(filepath.FromSlash(rel), "pom.xml")
	}
	abs := filepath.Join(root, pomFile)
	pom, err := readPom(abs)
	if err != nil {
		return
	}
	mod := ModuleInfo{
		Path:       rel,
		ArtifactID: pom.ArtifactID,
		GroupID:    pom.effectiveGroup(),
		Version:    pom.effectiveVersion(),
		Packaging:  pom.Packaging,
		Name:       pom.Name,
	}
	if mod.Packaging == "" {
		mod.Packaging = "jar"
	}
	if mod.Name == "" {
		mod.Name = mod.ArtifactID
	}
	if rel == "" {
		info.RootModule = &mod
		if lvl := pom.languageLevel(); lvl != "" {
			info.LanguageLevel = lvl
		}
		for _, d := range pom.Dependencies {
			if d.GroupID == "" || d.ArtifactID == "" {
				continue
			}
			if len(info.Dependencies) >= maxDeps {
				info.DependencyTrunc = true
				break
			}
			ver := d.Version
			coord := d.GroupID + ":" + d.ArtifactID
			if ver != "" {
				coord += ":" + ver
			}
			scope := d.Scope
			if scope == "" {
				scope = "compile"
			}
			info.Dependencies = append(info.Dependencies, DependencyInfo{
				GroupID:    d.GroupID,
				ArtifactID: d.ArtifactID,
				Version:    ver,
				Scope:      scope,
				Coord:      coord,
			})
		}
	} else {
		info.Modules = append(info.Modules, mod)
	}
	if depth >= maxModuleDepth {
		return
	}
	for _, m := range pom.Modules {
		m = path.Clean(strings.TrimSpace(strings.ReplaceAll(m, "\\", "/")))
		if m == "" || m == "." || strings.HasPrefix(m, "..") {
			continue
		}
		child := m
		if rel != "" {
			child = path.Join(rel, m)
		}
		fillMaven(info, root, child, depth+1)
	}
}

var (
	jdkOnce sync.Once
	jdkInfo JDKInfo
)

func detectJDK() JDKInfo {
	jdkOnce.Do(func() {
		jdkInfo = probeJDK()
	})
	return jdkInfo
}

func probeJDK() JDKInfo {
	info := JDKInfo{Home: strings.TrimSpace(os.Getenv("JAVA_HOME"))}
	javaBin := "java"
	if info.Home != "" {
		candidate := filepath.Join(info.Home, "bin", "java")
		if _, err := os.Stat(candidate); err == nil {
			javaBin = candidate
		}
	}
	ctx := exec.Command(javaBin, "-XshowSettings:properties", "-version")
	var stderr bytes.Buffer
	ctx.Stdout = &stderr
	ctx.Stderr = &stderr
	_ = ctx.Run()
	out := stderr.String()
	info.Version = parseJavaVersion(out)
	info.Vendor = parseJavaProp(out, "java.vendor")
	if info.Home == "" {
		info.Home = parseJavaProp(out, "java.home")
	}
	if info.Version == "" {
		// Fallback: java -version first line
		cmd := exec.Command(javaBin, "-version")
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		_ = cmd.Run()
		info.Version = parseJavaVersion(buf.String())
	}
	return info
}

func parseJavaProp(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		prefix := key + " = "
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

var verRe = regexp.MustCompile(`(?i)version\s+"([^"]+)"`)
var verRe2 = regexp.MustCompile(`(?i)java\.version = (.+)`)

func parseJavaVersion(out string) string {
	if m := verRe2.FindStringSubmatch(out); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	if m := verRe.FindStringSubmatch(out); len(m) == 2 {
		return m[1]
	}
	return ""
}

// WarmJDK kicks detection early (optional; Inspect also triggers it).
func WarmJDK() {
	_ = detectJDK()
	// touch time so unused import time stays if we add TTL later
	_ = time.Now()
}
