/*
===========================================================================

main.go - sro-packer, the server-side client update checker and patch builder

Compares the server's built client assets against updates/baseline.json and
existing PatchNNN.temir archives, packaging any changed or newly added files
into updates/PatchNNN.temir and updating updates/patches.json for sro-agent to
serve at /updates/*.

===========================================================================
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"opensro.online/server/internal/agent/updates"
	"opensro.online/server/internal/testsupport/licensed"
)

/*
================
resolveDefaultAssetsDir
================
*/
func resolveDefaultAssetsDir() string {
	if env := strings.TrimSpace(os.Getenv("SRO_CLIENT_ASSETS_DIR")); env != "" {
		return env
	}
	if resolved, err := licensed.ClientPublicRoot(); err == nil {
		if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
			return resolved
		}
	}
	if info, err := os.Stat("client-public"); err == nil && info.IsDir() {
		return "client-public"
	}
	return ""
}

/*
================
resolveDefaultDistDir
================
*/
func resolveDefaultDistDir() string {
	if env := strings.TrimSpace(os.Getenv("SRO_CLIENT_DIST_DIR")); env != "" {
		return env
	}
	for _, candidate := range []string{
		"client-dist",
		filepath.Join("..", "client-next", "temp", "artifacts", "dist"),
		filepath.Join("apps", "client-next", "temp", "artifacts", "dist"),
		filepath.Join("..", "client-next", "dist"),
		filepath.Join("apps", "client-next", "dist"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

/*
================
runVerify
================
*/
func runVerify(dir, key string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	verified := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".temir") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		_, toc, err := updates.ReadArchiveTOC(path, key)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		fmt.Printf("  [OK] %-18s (%d dosya)\n", entry.Name(), len(toc))
		verified++
	}
	fmt.Printf("Dogrulanan .temir arsivi: %d\n", verified)
	return nil
}

/*
================
main
================
*/
func main() {
	cmd := "sync"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("sro-packer", flag.ExitOnError)
	updatesDir := fs.String("updates", "updates", "Server updates directory (contains baseline.json, patches.json, PatchNNN.temir)")
	assetsDir := fs.String("assets", resolveDefaultAssetsDir(), "Built client-public asset directory")
	distDir := fs.String("dist", resolveDefaultDistDir(), "Compiled client-next frontend dist directory")
	temirDir := fs.String("temir-dir", "", "Directory containing base Media.temir, Data.temir, World.temir")
	key := fs.String("key", os.Getenv("SRO_ARCHIVE_KEY"), "TEMIR archive encryption passphrase")
	_ = fs.Parse(args)

	if *key == "" {
		*key = updates.DefaultArchiveKey
	}

	switch cmd {
	case "baseline":
		var (
			count int
			err   error
		)
		if strings.TrimSpace(*temirDir) != "" {
			count, err = updates.WriteBaselineFromArchives(*temirDir, *updatesDir, *key)
		} else {
			count, err = updates.WriteBaselineFromSources(*distDir, *assetsDir, *updatesDir)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "sro-packer baseline: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Baseline manifest hazir (%s/baseline.json, %d dosya).\n", *updatesDir, count)

	case "sync", "patch":
		res, err := updates.Sync(context.Background(), updates.SyncConfig{
			UpdatesDir:      *updatesDir,
			ClientPublicDir: *assetsDir,
			ClientDistDir:   *distDir,
			BaseArchivesDir: *temirDir,
			ArchiveKey:      *key,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "sro-packer sync: %v\n", err)
			os.Exit(1)
		}
		if res.CreatedPatch == "" {
			fmt.Printf("Sunucu guncel (degisen dosya yok, manifest v%d).\n", res.Version)
			return
		}
		fmt.Printf(
			"Yeni yama olusturuldu: %s (%d dosya, %d bayt, manifest v%d)\n",
			res.CreatedPatch,
			len(res.ChangedFiles),
			res.PatchBytes,
			res.Version,
		)
		for _, f := range res.ChangedFiles {
			fmt.Printf("  + /%s\n", f)
		}

	case "verify":
		target := *updatesDir
		if strings.TrimSpace(*temirDir) != "" {
			target = *temirDir
		}
		if err := runVerify(target, *key); err != nil {
			fmt.Fprintf(os.Stderr, "sro-packer verify: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Kullanim: sro-packer <sync|baseline|verify> [-updates dir] [-assets dir] [-dist dir] [-temir-dir dir]\n")
		os.Exit(1)
	}
}
