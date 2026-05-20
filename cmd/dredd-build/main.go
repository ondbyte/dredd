// Command dredd-build converts a Docker image into a Firecracker-bootable
// ext4 rootfs, injects the dreddagent binary as /sbin/dreddagent-init, and
// appends an entry to the languages JSON file.
//
// Requires: docker, mkfs.ext4, mount (run as root). Used only at setup time;
// the running dredd server does not need any of these.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ondbyte/dredd/langs"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "add":
		cmdAdd(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `dredd-build add --image IMAGE --id ID --source FILE --run CMD [--compile CMD]
                [--name NAME] [--version VER]
                [--size-mb N] [--rootfs-dir DIR]
                [--agent PATH] [--languages-file PATH]`)
	os.Exit(2)
}

func cmdAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	image := fs.String("image", "", "source Docker image (e.g. python:3.12)")
	id := fs.String("id", "", "language id, e.g. python-3.12")
	name := fs.String("name", "", "human-readable name")
	version := fs.String("version", "", "version label")
	sourceFile := fs.String("source", "", "source file name inside /work (e.g. main.py)")
	runCmd := fs.String("run", "", "run command (shell)")
	compileCmd := fs.String("compile", "", "compile command (optional)")
	sizeMB := fs.Int("size-mb", 1024, "rootfs ext4 image size in MB")
	rootfsDir := fs.String("rootfs-dir", envOr("DREDD_ROOTFS_DIR", "/var/lib/dredd/rootfs"), "directory to write rootfs file into")
	agentBin := fs.String("agent", envOr("DREDD_AGENT_BINARY", ""), "path to dreddagent host binary (required)")
	langsFile := fs.String("languages-file", envOr("DREDD_LANGUAGES_FILE", ""), "languages.json to append to (required)")
	_ = fs.Parse(args)

	if *image == "" || *id == "" || *sourceFile == "" || *runCmd == "" || *agentBin == "" || *langsFile == "" {
		fs.Usage()
		os.Exit(2)
	}

	if err := os.MkdirAll(*rootfsDir, 0o755); err != nil {
		log.Fatalf("rootfs-dir: %v", err)
	}
	rootfsPath := filepath.Join(*rootfsDir, *id+".ext4")

	if err := buildRootfs(*image, *agentBin, rootfsPath, *sizeMB); err != nil {
		log.Fatalf("build rootfs: %v", err)
	}

	if err := appendLanguage(*langsFile, langs.Language{
		ID:         *id,
		Name:       firstNonEmpty(*name, *id),
		Version:    *version,
		Rootfs:     filepath.Base(rootfsPath),
		SourceFile: *sourceFile,
		CompileCmd: *compileCmd,
		RunCmd:     *runCmd,
	}); err != nil {
		log.Fatalf("update languages file: %v", err)
	}
	fmt.Printf("ok: %s -> %s\n", *id, rootfsPath)
}

func buildRootfs(image, agentBin, outPath string, sizeMB int) error {
	tmp, err := os.MkdirTemp("", "dredd-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// 1. Export the image's rootfs.
	cidFile := filepath.Join(tmp, "cid")
	if err := run("docker", "create", "--cidfile", cidFile, image, "true"); err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	cidBytes, err := os.ReadFile(cidFile)
	if err != nil {
		return err
	}
	cid := string(cidBytes)
	defer run("docker", "rm", "-f", cid)
	tarPath := filepath.Join(tmp, "rootfs.tar")
	if err := runRedirect(tarPath, "docker", "export", cid); err != nil {
		return fmt.Errorf("docker export: %w", err)
	}

	// 2. Create empty ext4 image and mount it.
	if err := run("dd", "if=/dev/zero", "of="+outPath, "bs=1M", fmt.Sprintf("count=%d", sizeMB), "status=none"); err != nil {
		return fmt.Errorf("dd: %w", err)
	}
	if err := run("mkfs.ext4", "-q", "-F", outPath); err != nil {
		return fmt.Errorf("mkfs.ext4: %w", err)
	}
	mnt := filepath.Join(tmp, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := run("mount", "-o", "loop", outPath, mnt); err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	defer run("umount", mnt)

	// 3. Untar the image rootfs into the mount.
	if err := run("tar", "-xf", tarPath, "-C", mnt); err != nil {
		return fmt.Errorf("untar: %w", err)
	}

	// 4. Install dreddagent as /sbin/dreddagent-init.
	if err := os.MkdirAll(filepath.Join(mnt, "sbin"), 0o755); err != nil {
		return err
	}
	if err := copyFile(agentBin, filepath.Join(mnt, "sbin", "dreddagent-init"), 0o755); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(mnt, "work"), 0o755); err != nil {
		return err
	}
	return nil
}

func appendLanguage(path string, l langs.Language) error {
	var list []langs.Language
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return fmt.Errorf("parse existing %s: %w", path, err)
		}
	}
	// Replace existing entry with same id, otherwise append.
	replaced := false
	for i, existing := range list {
		if existing.ID == l.ID {
			list[i] = l
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, l)
	}
	out, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runRedirect(outPath, name string, args ...string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(name, args...)
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
