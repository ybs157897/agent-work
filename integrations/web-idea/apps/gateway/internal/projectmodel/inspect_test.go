package projectmodel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectMavenSingle(t *testing.T) {
	dir := t.TempDir()
	pom := `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <version>1.0.0</version>
  <properties>
    <java.version>17</java.version>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
      <version>3.2.0</version>
    </dependency>
  </dependencies>
</project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	info := Inspect(dir)
	if info.Build != BuildMaven {
		t.Fatalf("build=%s", info.Build)
	}
	if info.RootModule == nil || info.RootModule.ArtifactID != "demo" {
		t.Fatalf("root=%+v", info.RootModule)
	}
	if info.LanguageLevel != "17" {
		t.Fatalf("lang=%s", info.LanguageLevel)
	}
	if len(info.Dependencies) != 1 || info.Dependencies[0].ArtifactID != "spring-boot-starter-web" {
		t.Fatalf("deps=%+v", info.Dependencies)
	}
}

func TestInspectMavenModules(t *testing.T) {
	dir := t.TempDir()
	rootPom := `<?xml version="1.0"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>parent</artifactId>
  <version>1.0.0</version>
  <packaging>pom</packaging>
  <modules>
    <module>api</module>
    <module>core</module>
  </modules>
</project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(rootPom), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"api", "core"} {
		sub := filepath.Join(dir, m)
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		child := `<project><modelVersion>4.0.0</modelVersion><artifactId>` + m + `</artifactId><parent><groupId>com.example</groupId><artifactId>parent</artifactId><version>1.0.0</version></parent></project>`
		if err := os.WriteFile(filepath.Join(sub, "pom.xml"), []byte(child), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := Inspect(dir)
	if len(info.Modules) != 2 {
		t.Fatalf("modules=%+v", info.Modules)
	}
}
