package server

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"go.uber.org/zap"
)

func (s *Server) refreshResourceStatus() error {
	dbSize, err := s.db.GetDBSize(context.Background())
	if err != nil {
		s.logger.Error("could not get db size", zap.Error(err))
		dbSize = -1
	}

	chainDir := s.cometbftConfig.RootDir
	chainSize := int64(0)
	err = filepath.Walk(chainDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			s.logger.Debug("error walking chain dir", zap.String("path", path), zap.Error(err))
			return nil // Continue walking despite errors
		}
		if !info.IsDir() {
			chainSize += info.Size()
		}
		return nil
	})
	if err != nil {
		s.logger.Error("could not calculate chain size", zap.Error(err))
		chainSize = -1
	}

	stat, err := mem.VirtualMemory()
	if err != nil {
		s.logger.Error("could not get memory info", zap.Error(err))
	}

	cpuStat, err := cpu.Percent(time.Second, false)
	if err != nil {
		s.logger.Error("could not get cpu info", zap.Error(err))
	}
	total := 0.0
	for _, perc := range cpuStat {
		total += perc
	}
	cpuUsage := int64(total / float64(len(cpuStat)))

	diskStat, err := disk.Usage(s.config.RootDir)
	if err != nil {
		s.logger.Error("could not get disk info", zap.String("path", s.config.RootDir), zap.Error(err))
	} else {
		s.logger.Debug(
			"disk stats",
			zap.String("path", s.config.RootDir),
			zap.Uint64("used_GB", diskStat.Used/(1024*1024*1024)),
			zap.Uint64("free_GB", diskStat.Free/(1024*1024*1024)),
			zap.Uint64("total_GB", diskStat.Total/(1024*1024*1024)),
		)
	}

	upsertCache(s.cache.resourceInfo, ResourceInfoKey, func(resourceInfo *v1.GetStatusResponse_ResourceInfo) *v1.GetStatusResponse_ResourceInfo {
		resourceInfo.DbSize = dbSize
		resourceInfo.ChainSize = chainSize
		resourceInfo.MemSize = int64(stat.Total)
		resourceInfo.MemUsage = int64(stat.Used)
		resourceInfo.CpuUsage = cpuUsage
		resourceInfo.DiskUsage = int64(diskStat.Used)
		resourceInfo.DiskFree = int64(diskStat.Free)
		return resourceInfo
	})

	return nil
}
