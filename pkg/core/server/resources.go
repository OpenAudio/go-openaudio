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
)

func (s *Server) refreshResourceStatus() error {
	dbSize, err := s.db.GetDBSize(context.Background())
	if err != nil {
		s.logger.Errorf("could not get db size: %v", err)
		dbSize = -1
	}

	chainDir := s.cometbftConfig.RootDir
	chainSize := int64(-1)
	filepath.Walk(chainDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		chainSize += info.Size()
		return nil
	})

	stat, err := mem.VirtualMemory()
	if err != nil {
		s.logger.Errorf("could not get memory info: %v", err)
	}

	cpuStat, err := cpu.Percent(time.Second, false)
	if err != nil {
		s.logger.Errorf("could not get cpu info: %v", err)
	}
	total := 0.0
	for _, perc := range cpuStat {
		total += perc
	}
	cpuUsage := int64(total / float64(len(cpuStat)))

	diskStat, err := disk.Usage(s.config.RootDir)
	if err != nil {
		s.logger.Errorf("could not get disk info: %v", err)
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
