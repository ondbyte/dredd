package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type PoolStrategy string

const (
	PoolPrewarmedPerLang  PoolStrategy = "prewarmed_per_lang"
	PoolOnDemandSpare     PoolStrategy = "ondemand_spare"
	PoolPrewarmedGeneric  PoolStrategy = "prewarmed_generic"
)

type Config struct {
	HTTPAddr           string
	RedisURL           string
	LanguagesFile      string
	KernelPath         string
	RootfsDir          string
	AgentBinary        string
	GenericRootfs      string // used by prewarmed_generic strategy
	PoolStrategy       PoolStrategy
	PoolSize           int
	WorkerConcurrency  int
	ResultTTL          time.Duration
	VMVcpus            int
	VMMemMB            int
	DefaultTimeLimit   time.Duration
	DefaultMemoryLimit int
	OutputLimitBytes   int
	FirecrackerBin     string
}

func FromEnv() (*Config, error) {
	c := &Config{
		HTTPAddr:           env("DREDD_HTTP_ADDR", ":8080"),
		RedisURL:           env("DREDD_REDIS_URL", "redis://localhost:6379/0"),
		LanguagesFile:      env("DREDD_LANGUAGES_FILE", ""),
		KernelPath:         env("DREDD_KERNEL_PATH", ""),
		RootfsDir:          env("DREDD_ROOTFS_DIR", "/var/lib/dredd/rootfs"),
		AgentBinary:        env("DREDD_AGENT_BINARY", ""),
		GenericRootfs:      env("DREDD_GENERIC_ROOTFS", ""),
		PoolStrategy:       PoolStrategy(env("DREDD_POOL_STRATEGY", string(PoolPrewarmedPerLang))),
		PoolSize:           envInt("DREDD_POOL_SIZE", 1),
		WorkerConcurrency:  envInt("DREDD_WORKER_CONCURRENCY", 4),
		ResultTTL:          time.Duration(envInt("DREDD_RESULT_TTL_SECONDS", 300)) * time.Second,
		VMVcpus:            envInt("DREDD_VM_VCPUS", 1),
		VMMemMB:            envInt("DREDD_VM_MEM_MB", 256),
		DefaultTimeLimit:   time.Duration(envInt("DREDD_DEFAULT_TIME_LIMIT_MS", 2000)) * time.Millisecond,
		DefaultMemoryLimit: envInt("DREDD_DEFAULT_MEMORY_LIMIT_MB", 256),
		OutputLimitBytes:   envInt("DREDD_OUTPUT_LIMIT_BYTES", 1<<20),
		FirecrackerBin:     env("DREDD_FIRECRACKER_BIN", "firecracker"),
	}

	if c.LanguagesFile == "" {
		return nil, fmt.Errorf("DREDD_LANGUAGES_FILE is required")
	}
	if c.KernelPath == "" {
		return nil, fmt.Errorf("DREDD_KERNEL_PATH is required")
	}
	switch c.PoolStrategy {
	case PoolPrewarmedPerLang, PoolOnDemandSpare, PoolPrewarmedGeneric:
	default:
		return nil, fmt.Errorf("DREDD_POOL_STRATEGY invalid: %q", c.PoolStrategy)
	}
	if c.PoolStrategy == PoolPrewarmedGeneric && c.GenericRootfs == "" {
		return nil, fmt.Errorf("DREDD_GENERIC_ROOTFS required when DREDD_POOL_STRATEGY=prewarmed_generic")
	}
	if c.PoolSize < 1 {
		c.PoolSize = 1
	}
	if c.WorkerConcurrency < 1 {
		c.WorkerConcurrency = 1
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
