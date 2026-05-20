package dredd_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/ondbyte/dredd/dreddtest"
	"github.com/ondbyte/dredd/langs"
)

// langCase describes one entry in the Judge0-style language matrix.
//
// Every entry's Source must produce exactly "hello-<ID>\n" on stdout. The
// test asserts that contract so the matrix can stay uniform.
//
// Two execution paths are supported:
//   - LocalExecutor (TestEndToEnd_AllLanguages): runs CompileCmd/RunCmd on
//     the host via /bin/sh -c. ProbeBins are checked via exec.LookPath; if
//     none are present the subtest skips.
//   - DockerExecutor (TestEndToEnd_AllLanguagesDocker): runs the case in a
//     transient `docker run` against Image. Tests fail rather than skip if
//     the image is unavailable, matching dredd's per-rootfs isolation
//     model.
type langCase struct {
	ID         string
	Name       string
	ProbeBins  []string // host PATH probe; only used by LocalExecutor test
	Image      string   // Docker image used by DockerExecutor test
	SourceFile string
	// CompileCmd / RunCmd are used by the LocalExecutor variant. They
	// typically reference host-specific binaries (python3.12, node22).
	CompileCmd string
	RunCmd     string
	// DockerCompileCmd / DockerRunCmd override the above when the docker
	// image uses different binary names (python:3.12 image only has
	// `python`, node:22 only has `node`, etc.). If empty, CompileCmd /
	// RunCmd are reused.
	DockerCompileCmd string
	DockerRunCmd     string
	Source           string
}

func (c langCase) expected() string { return "hello-" + c.ID + "\n" }

// hostLanguage returns the langs.Language used for LocalExecutor tests
// (host PATH).
func (c langCase) hostLanguage() langs.Language {
	return langs.Language{
		ID:         c.ID,
		Name:       c.Name,
		Rootfs:     "fake.ext4",
		SourceFile: c.SourceFile,
		CompileCmd: c.CompileCmd,
		RunCmd:     c.RunCmd,
	}
}

// dockerLanguage returns the langs.Language used for the DockerExecutor
// tests. CompileCmd / RunCmd are taken from the Docker-mode overrides
// when present.
func (c langCase) dockerLanguage() langs.Language {
	cc := c.CompileCmd
	if c.DockerCompileCmd != "" {
		cc = c.DockerCompileCmd
	}
	rc := c.RunCmd
	if c.DockerRunCmd != "" {
		rc = c.DockerRunCmd
	}
	return langs.Language{
		ID:         c.ID,
		Name:       c.Name,
		Rootfs:     "fake.ext4",
		SourceFile: c.SourceFile,
		CompileCmd: cc,
		RunCmd:     rc,
	}
}

// --- family helpers ---------------------------------------------------------

func cCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "C — " + localBin,
		ProbeBins:  []string{localBin},
		Image:      image,
		SourceFile: "main.c",
		// Host (LocalExecutor) uses the version-specific binary; the gcc:N
		// Docker image only ships a generic `gcc`.
		CompileCmd:       localBin + " main.c -O0 -o prog",
		DockerCompileCmd: "gcc main.c -O0 -o prog",
		RunCmd:           "./prog",
		Source: `#include <stdio.h>
int main(void) { printf("hello-` + id + `\n"); return 0; }
`,
	}
}

func cppCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "C++ — " + localBin,
		ProbeBins:        []string{localBin},
		Image:            image,
		SourceFile:       "main.cpp",
		CompileCmd:       localBin + " main.cpp -O0 -o prog",
		DockerCompileCmd: "g++ main.cpp -O0 -o prog",
		RunCmd:           "./prog",
		Source: `#include <cstdio>
int main() { std::printf("hello-` + id + `\n"); return 0; }
`,
	}
}

func goCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "Go — " + localBin,
		ProbeBins:    []string{localBin},
		Image:        image,
		SourceFile:   "main.go",
		RunCmd:       localBin + " run main.go", // host: go1.18 (etc.)
		DockerRunCmd: "go run main.go",          // container: plain `go`
		Source: `package main
import "fmt"
func main() { fmt.Println("hello-` + id + `") }
`,
	}
}

func pythonCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "Python — " + localBin,
		ProbeBins:    []string{localBin},
		Image:        image,
		SourceFile:   "main.py",
		RunCmd:       localBin + " main.py",
		DockerRunCmd: "python main.py",
		Source: `from __future__ import print_function
print("hello-` + id + `")
`,
	}
}

func nodeCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "JavaScript (Node.js) — " + localBin,
		ProbeBins:    []string{localBin},
		Image:        image,
		SourceFile:   "main.js",
		RunCmd:       localBin + " main.js",
		DockerRunCmd: "node main.js",
		Source:       `console.log("hello-` + id + `");` + "\n",
	}
}

func phpCase(id, localBin, image string) langCase {
	return langCase{
		ID: id, Name: "PHP — " + localBin,
		ProbeBins:    []string{localBin},
		Image:        image,
		SourceFile:   "main.php",
		RunCmd:       localBin + " main.php",
		DockerRunCmd: "php main.php",
		Source:       "<?php echo \"hello-" + id + "\\n\";\n",
	}
}

func rustCase(id, image string) langCase {
	return langCase{
		ID: id, Name: "Rust — " + id,
		ProbeBins:  []string{"rustc"},
		Image:      image,
		SourceFile: "main.rs",
		CompileCmd: "rustc main.rs -o prog",
		RunCmd:     "./prog",
		Source:     "fn main() { println!(\"hello-" + id + "\"); }\n",
	}
}

func scalaCase(id, image string) langCase {
	return langCase{
		ID: id, Name: "Scala — " + id,
		ProbeBins:  []string{"scala"},
		Image:      image,
		SourceFile: "main.scala",
		RunCmd:     "scala main.scala",
		Source: `object Main extends App { println("hello-` + id + `") }
`,
	}
}

func kotlinCase(id, image string) langCase {
	return langCase{
		ID: id, Name: "Kotlin — " + id,
		ProbeBins:  []string{"kotlinc"},
		Image:      image,
		SourceFile: "main.kt",
		CompileCmd: "kotlinc main.kt -include-runtime -d main.jar 2>/dev/null",
		RunCmd:     "java -jar main.jar",
		Source:     "fun main() { println(\"hello-" + id + "\") }\n",
	}
}

func tsCase(id, version, image string) langCase {
	return langCase{
		ID: id, Name: "TypeScript — " + id,
		ProbeBins:  []string{"tsc"},
		Image:      image,
		SourceFile: "main.ts",
		// Install the specific typescript version, compile, run with node.
		CompileCmd: "npm install --silent --no-audit --no-fund typescript@" + version + " >/dev/null 2>&1 && npx --no-install tsc main.ts",
		RunCmd:     "node main.js",
		Source:     `console.log("hello-` + id + `");` + "\n",
	}
}

func rCase(id, image string) langCase {
	return langCase{
		ID: id, Name: "R — " + id,
		ProbeBins:  []string{"Rscript"},
		Image:      image,
		SourceFile: "main.R",
		RunCmd:     "Rscript main.R",
		Source:     "cat(\"hello-" + id + "\\n\")\n",
	}
}

// --- the full catalogue -----------------------------------------------------

// Image policy:
//   - Prefer official upstream images: gcc:N, python:N, node:N, rust:N,
//     openjdk:N, php:N-cli, ruby:N, etc.
//   - For languages/versions without a usable public image, the catalogue
//     references a custom image under dredd-test/<id>:latest; Dockerfiles
//     for those live in dreddtest/images/ and are built by
//     `dreddtest/images/build.sh`.
func languageCases() []langCase {
	cases := []langCase{
		{
			ID: "assembly-nasm", Name: "Assembly (NASM)",
			ProbeBins:  []string{"nasm"},
			Image:      "dredd-test/assembly-nasm:latest",
			SourceFile: "main.asm",
			CompileCmd: "nasm -f elf64 main.asm -o main.o && ld main.o -o prog",
			RunCmd:     "./prog",
			Source: `BITS 64
section .data
msg: db "hello-assembly-nasm",10
mlen equ $ - msg
section .text
global _start
_start:
    mov rax,1
    mov rdi,1
    mov rsi,msg
    mov rdx,mlen
    syscall
    mov rax,60
    xor rdi,rdi
    syscall
`,
		},
		{
			ID: "bash", Name: "Bash",
			ProbeBins:  []string{"bash"},
			Image:      "bash:5",
			SourceFile: "main.sh",
			RunCmd:     "bash main.sh",
			Source:     "echo hello-bash\n",
		},
		{
			ID: "freebasic", Name: "Basic (FreeBASIC)",
			ProbeBins:  []string{"fbc"},
			Image:      "dredd-test/freebasic:latest",
			SourceFile: "main.bas",
			CompileCmd: "fbc main.bas -x prog",
			RunCmd:     "./prog",
			Source:     "Print \"hello-freebasic\"\n",
		},
	}

	// C — 7 GCC versions, 2 Clang versions.
	for _, v := range []struct{ bin, image string }{
		{"gcc-7", "gcc:7"},
		{"gcc-8", "gcc:8"},
		{"gcc-9", "gcc:9"},
		{"gcc-10", "gcc:10"},
		{"gcc-12", "gcc:12"},
		{"gcc-13", "gcc:13"},
		{"gcc-14", "gcc:14"},
	} {
		cases = append(cases, cCase("c-"+v.bin, v.bin, v.image))
	}
	for _, v := range []struct{ bin, image string }{
		{"clang-7", "silkeh/clang:7"},
		{"clang-19", "silkeh/clang:19"},
	} {
		c := cCase("c-"+v.bin, v.bin, v.image)
		// silkeh/clang:N image only has `clang`; override the docker
		// compile cmd from the `gcc` default cCase sets to `clang`.
		c.DockerCompileCmd = "clang main.c -O0 -o prog"
		cases = append(cases, c)
	}

	// C++ — 4 GCC + 1 Clang.
	for _, v := range []struct{ bin, image string }{
		{"g++-7", "gcc:7"},
		{"g++-9", "gcc:9"},
		{"g++-13", "gcc:13"},
		{"g++-14", "gcc:14"},
	} {
		cases = append(cases, cppCase("cpp-"+v.bin, v.bin, v.image))
	}
	{
		c := cppCase("cpp-clang++-7", "clang++-7", "silkeh/clang:7")
		c.DockerCompileCmd = "clang++ main.cpp -O0 -o prog"
		cases = append(cases, c)
	}

	cases = append(cases,
		// C# (Mono)
		langCase{
			ID: "csharp-mono", Name: "C# (Mono)",
			ProbeBins:  []string{"mcs"},
			Image:      "mono:6",
			SourceFile: "main.cs",
			CompileCmd: "mcs main.cs -out:main.exe",
			RunCmd:     "mono main.exe",
			Source: `class Hello {
    static void Main() { System.Console.WriteLine("hello-csharp-mono"); }
}
`,
		},
		// Clojure
		langCase{
			ID: "clojure", Name: "Clojure",
			ProbeBins:  []string{"clojure", "clj"},
			Image:      "clojure:temurin-21-tools-deps",
			SourceFile: "main.clj",
			RunCmd:     "clojure -M main.clj",
			Source:     `(println "hello-clojure")` + "\n",
		},
		// COBOL (GnuCOBOL)
		langCase{
			ID: "cobol", Name: "COBOL (GnuCOBOL)",
			ProbeBins:  []string{"cobc"},
			Image:      "dredd-test/cobol:latest",
			SourceFile: "main.cob",
			CompileCmd: "cobc -x -free main.cob -o prog",
			RunCmd:     "./prog",
			Source: `IDENTIFICATION DIVISION.
PROGRAM-ID. HELLO.
PROCEDURE DIVISION.
    DISPLAY "hello-cobol".
    STOP RUN.
`,
		},
		// Common Lisp (SBCL)
		langCase{
			ID: "common-lisp-sbcl", Name: "Common Lisp (SBCL)",
			ProbeBins:  []string{"sbcl"},
			Image:      "clfoundation/sbcl:latest",
			SourceFile: "main.lisp",
			RunCmd:     "sbcl --script main.lisp",
			Source:     `(format t "hello-common-lisp-sbcl~%")` + "\n",
		},
		// D (DMD)
		langCase{
			ID: "d-dmd", Name: "D (DMD)",
			ProbeBins:  []string{"dmd"},
			Image:      "dlang2/dmd-ubuntu:latest",
			SourceFile: "main.d",
			CompileCmd: "dmd main.d -of=prog",
			RunCmd:     "./prog",
			Source: `import std.stdio;
void main() { writeln("hello-d-dmd"); }
`,
		},
		// Dart
		langCase{
			ID: "dart", Name: "Dart",
			ProbeBins:  []string{"dart"},
			Image:      "dart:stable",
			SourceFile: "main.dart",
			RunCmd:     "dart main.dart",
			Source:     `void main() { print("hello-dart"); }` + "\n",
		},
		// Elixir
		langCase{
			ID: "elixir", Name: "Elixir",
			ProbeBins:  []string{"elixir"},
			Image:      "elixir:1.16",
			SourceFile: "main.exs",
			RunCmd:     "elixir main.exs",
			Source:     `IO.puts "hello-elixir"` + "\n",
		},
		// Erlang (escript)
		langCase{
			ID: "erlang", Name: "Erlang (escript)",
			ProbeBins:  []string{"escript"},
			Image:      "erlang:26",
			SourceFile: "main.erl",
			RunCmd:     "escript main.erl",
			Source: `#!/usr/bin/env escript
main(_) -> io:format("hello-erlang~n").
`,
		},
		// F# (dotnet fsi over a script file)
		langCase{
			ID: "fsharp", Name: "F# (.NET fsi)",
			ProbeBins:  []string{"dotnet"},
			Image:      "mcr.microsoft.com/dotnet/sdk:8.0",
			SourceFile: "main.fsx",
			RunCmd:     "dotnet fsi main.fsx",
			Source:     `printfn "hello-fsharp"` + "\n",
		},
		// Fortran (gfortran ships in gcc images)
		langCase{
			ID: "fortran", Name: "Fortran (gfortran)",
			ProbeBins:  []string{"gfortran"},
			Image:      "gcc:13",
			SourceFile: "main.f90",
			CompileCmd: "gfortran main.f90 -o prog",
			RunCmd:     "./prog",
			Source: `program hello
  write(*,'(A)') "hello-fortran"
end program hello
`,
		},
	)

	// Go — 4 versions, each from its own golang:1.X image.
	cases = append(cases,
		goCase("go-1.13", "go1.13", "golang:1.13"),
		goCase("go-1.18", "go1.18", "golang:1.18"),
		goCase("go-1.21", "go1.21", "golang:1.21"),
		goCase("go-1.23", "go1.23", "golang:1.23"),
	)

	cases = append(cases,
		// Groovy
		langCase{
			ID: "groovy", Name: "Groovy",
			ProbeBins:  []string{"groovy"},
			Image:      "groovy:4-jdk17",
			SourceFile: "main.groovy",
			RunCmd:     "groovy main.groovy",
			Source:     `println "hello-groovy"` + "\n",
		},
		// Haskell (GHC)
		langCase{
			ID: "haskell-ghc", Name: "Haskell (GHC)",
			ProbeBins:  []string{"runghc", "runhaskell", "ghc"},
			Image:      "haskell:9.6",
			SourceFile: "main.hs",
			RunCmd:     "runghc main.hs",
			Source:     `main = putStrLn "hello-haskell-ghc"` + "\n",
		},
		// Java — OpenJDK 13. The official `openjdk:13` tag has been
		// removed; use Liberica's debian-based JDK 13 instead.
		langCase{
			ID: "java-openjdk-13", Name: "Java (OpenJDK 13)",
			ProbeBins:  []string{"javac"},
			Image:      "bellsoft/liberica-openjdk-debian:13",
			SourceFile: "Main.java",
			CompileCmd: "javac Main.java",
			RunCmd:     "java Main",
			Source: `public class Main {
    public static void main(String[] args) {
        System.out.println("hello-java-openjdk-13");
    }
}
`,
		},
		// Java — JDK 17 (Temurin)
		langCase{
			ID: "java-jdk-17", Name: "Java (JDK 17)",
			ProbeBins:  []string{"javac"},
			Image:      "eclipse-temurin:17-jdk",
			SourceFile: "Main.java",
			CompileCmd: "javac Main.java",
			RunCmd:     "java Main",
			Source: `public class Main {
    public static void main(String[] args) {
        System.out.println("hello-java-jdk-17");
    }
}
`,
		},
		// JavaFX — Liberica JDK with JavaFX bundled.
		langCase{
			ID: "javafx", Name: "JavaFX",
			ProbeBins:  []string{"javafx-launcher"},
			Image:      "bellsoft/liberica-openjdk-debian:17",
			SourceFile: "Main.java",
			// Even with a JavaFX-bundled JDK, full UI bring-up needs a
			// display. We do not exercise JavaFX widgets — we just verify
			// the toolchain is reachable. The program imports a
			// javafx.application package symbol and prints the line.
			CompileCmd: "javac Main.java",
			RunCmd:     "java Main",
			Source: `public class Main {
    public static void main(String[] args) {
        Class<?> ignored = javafx.application.Platform.class;
        System.out.println("hello-javafx");
    }
}
`,
		},
	)

	// JavaScript (Node) — 4 versions.
	cases = append(cases,
		nodeCase("nodejs-12", "node12", "node:12"),
		nodeCase("nodejs-16", "node16", "node:16"),
		nodeCase("nodejs-20", "node20", "node:20"),
		nodeCase("nodejs-22", "node22", "node:22"),
	)

	// Kotlin — 2 versions. No usable public Kotlin image exists on Docker
	// Hub, so both shipping in dredd-test custom images.
	cases = append(cases,
		kotlinCase("kotlin-1.3", "dredd-test/kotlin-1.3:latest"),
		kotlinCase("kotlin-2.1", "dredd-test/kotlin-2.1:latest"),
	)

	cases = append(cases,
		// Lua
		langCase{
			ID: "lua", Name: "Lua",
			ProbeBins:  []string{"lua"},
			Image:      "nickblah/lua:5.4",
			SourceFile: "main.lua",
			RunCmd:     "lua main.lua",
			Source:     `print("hello-lua")` + "\n",
		},
		// Objective-C — needs GNUstep on Linux. Custom image.
		langCase{
			ID: "objective-c", Name: "Objective-C",
			ProbeBins:  []string{"gnustep-config"},
			Image:      "dredd-test/objective-c:latest",
			SourceFile: "main.m",
			CompileCmd: "gcc -x objective-c main.m -o prog -lobjc",
			RunCmd:     "./prog",
			Source: `#include <stdio.h>
#include <objc/runtime.h>
int main(void) { (void)objc_getClass("NSObject"); printf("hello-objective-c\n"); return 0; }
`,
		},
		// OCaml
		langCase{
			ID: "ocaml", Name: "OCaml",
			ProbeBins:  []string{"ocaml"},
			Image:      "ocaml/opam:debian-12-ocaml-5.1",
			SourceFile: "main.ml",
			RunCmd:     "ocaml main.ml",
			Source:     `print_endline "hello-ocaml"` + "\n",
		},
		// Octave
		langCase{
			ID: "octave", Name: "Octave",
			ProbeBins:  []string{"octave-cli", "octave"},
			Image:      "mtmiller/octave:latest",
			SourceFile: "main.m",
			RunCmd:     "octave-cli -q main.m",
			Source:     `disp("hello-octave")` + "\n",
		},
		// Pascal (Free Pascal Compiler)
		langCase{
			ID: "pascal-fpc", Name: "Pascal (FPC)",
			ProbeBins:  []string{"fpc"},
			Image:      "dredd-test/pascal-fpc:latest",
			SourceFile: "main.pas",
			CompileCmd: "fpc main.pas -oprog",
			RunCmd:     "./prog",
			Source: `program Hello;
begin
  writeln('hello-pascal-fpc');
end.
`,
		},
		// Perl
		langCase{
			ID: "perl", Name: "Perl",
			ProbeBins:  []string{"perl"},
			Image:      "perl:5.38",
			SourceFile: "main.pl",
			RunCmd:     "perl main.pl",
			Source:     `print "hello-perl\n";` + "\n",
		},
	)

	// PHP — 2 versions.
	cases = append(cases,
		phpCase("php-7.4", "php7.4", "php:7.4-cli"),
		phpCase("php-8.3", "php8.3", "php:8.3-cli"),
	)

	cases = append(cases,
		// Prolog (SWI-Prolog)
		langCase{
			ID: "prolog-swi", Name: "Prolog (SWI)",
			ProbeBins:  []string{"swipl"},
			Image:      "swipl:stable",
			SourceFile: "main.pl",
			RunCmd:     "swipl -q -t halt -g main main.pl",
			Source: `:- initialization(main).
main :- write('hello-prolog-swi'), nl.
`,
		},
	)

	// Python — 6 versions.
	cases = append(cases,
		pythonCase("python-2.7", "python2.7", "python:2.7"),
		pythonCase("python-3.8", "python3.8", "python:3.8"),
		pythonCase("python-3.11", "python3.11", "python:3.11"),
		pythonCase("python-3.12", "python3.12", "python:3.12"),
		pythonCase("python-3.13", "python3.13", "python:3.13"),
		pythonCase("python-3.14", "python3.14", "python:3.14-rc"),
	)

	// R — 2 versions.
	cases = append(cases,
		rCase("r-4.0", "r-base:4.0.5"),
		rCase("r-4.4", "r-base:4.4.0"),
	)

	cases = append(cases,
		// Ruby
		langCase{
			ID: "ruby", Name: "Ruby",
			ProbeBins:  []string{"ruby"},
			Image:      "ruby:3.3",
			SourceFile: "main.rb",
			RunCmd:     "ruby main.rb",
			Source:     `puts "hello-ruby"` + "\n",
		},
	)

	// Rust — 2 versions.
	cases = append(cases,
		rustCase("rust-1.40", "rust:1.40"),
		rustCase("rust-1.85", "rust:1.85"),
	)

	// Scala — 2 versions.
	cases = append(cases,
		scalaCase("scala-2.13", "sbtscala/scala-sbt:eclipse-temurin-jammy-17.0.10_7_1.10.0_2.13.13"),
		scalaCase("scala-3.4", "sbtscala/scala-sbt:eclipse-temurin-jammy-17.0.10_7_1.10.0_3.4.2"),
	)

	cases = append(cases,
		// SQL (SQLite)
		langCase{
			ID: "sqlite", Name: "SQL (SQLite)",
			ProbeBins:  []string{"sqlite3"},
			Image:      "keinos/sqlite3:latest",
			SourceFile: "main.sql",
			RunCmd:     "sqlite3 < main.sql",
			Source:     `SELECT 'hello-sqlite';` + "\n",
		},
		// Swift
		langCase{
			ID: "swift", Name: "Swift",
			ProbeBins:  []string{"swift"},
			Image:      "swift:5.10",
			SourceFile: "main.swift",
			RunCmd:     "swift main.swift",
			Source:     `print("hello-swift")` + "\n",
		},
	)

	// TypeScript — 3 versions; each installs its tsc into a fresh
	// node:20 image at compile time.
	cases = append(cases,
		tsCase("typescript-3.7", "3.7", "node:20"),
		tsCase("typescript-5.0", "5.0", "node:20"),
		tsCase("typescript-5.6", "5.6", "node:20"),
	)

	cases = append(cases,
		// Visual Basic.NET (Mono vbnc)
		langCase{
			ID: "vbnet", Name: "Visual Basic.NET (Mono)",
			ProbeBins:  []string{"vbnc"},
			Image:      "mono:6",
			SourceFile: "main.vb",
			CompileCmd: "vbnc main.vb",
			RunCmd:     "mono main.exe",
			Source: `Module Hello
    Sub Main()
        System.Console.WriteLine("hello-vbnet")
    End Sub
End Module
`,
		},
	)

	return cases
}

// TestEndToEnd_AllLanguages exercises every entry against the host's
// LocalExecutor. Subtests whose toolchain isn't on PATH are skipped — this
// is the lightweight variant useful in unit-test CI.
//
// The all-languages-must-run variant is TestEndToEnd_AllLanguagesDocker.
func TestEndToEnd_AllLanguages(t *testing.T) {
	cases := languageCases()
	catalogue := make([]langs.Language, 0, len(cases))
	for _, c := range cases {
		catalogue = append(catalogue, c.hostLanguage())
	}
	i := startInfraWithLanguages(t, dreddtest.LocalExecutor{}, catalogue)

	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			var foundBin string
			for _, b := range c.ProbeBins {
				if _, err := exec.LookPath(b); err == nil {
					foundBin = b
					break
				}
			}
			if foundBin == "" {
				t.Skipf("none of %v on PATH", c.ProbeBins)
			}

			id := submit(t, i, map[string]any{
				"language":      c.ID,
				"source":        c.Source,
				"stdins":        []string{""},
				"time_limit_ms": 60000,
			})
			status := waitForStatus(t, i, id, "done", 120*time.Second)
			results, _ := status["results"].([]any)
			if len(results) != 1 {
				t.Fatalf("want 1 result, got %d: %+v", len(results), status)
			}
			r := results[0].(map[string]any)
			got, _ := r["stdout"].(string)
			if got != c.expected() {
				t.Errorf("stdout = %q, want %q (stderr=%q, compile_error=%q, exit=%v)",
					got, c.expected(), r["stderr"], status["compile_error"], r["exit_code"])
			}
		})
	}
}
