package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/store"
)

// 批量升级：在单点升级链路（startUpgradeFor）外加一层内存任务编排。
// 与 upgradeStates 同生命周期：PM 重启丢批量视图，但进行中的单点升级不受影响，
// 每台节点最终成功仍以重连 HELLO 版本（upgradeStates=verified）为准。

const (
	batchPhaseRunning = "running" // 执行中（含金丝雀阶段）
	batchPhaseDone    = "done"    // 全部节点到达终态
	maxBatchTasks     = 20        // 内存中保留的批量任务上限（超出淘汰最旧的已结束任务）
)

// batchNode 单个节点在批量任务中的计划与进展。
// Stage: pending(待执行)/skipped(跳过)/running(已启动)/verified(新版本重连)/failed
type batchNode struct {
	Username       string `json:"username"`
	GoOS           string `json:"goos"`
	GoArch         string `json:"goarch"`
	CurrentVersion string `json:"currentVersion"`
	VersionID      int64  `json:"versionId"`
	Version        string `json:"version"` // 目标版本
	Role           string `json:"role,omitempty"`
	Stage          string `json:"stage"`
	Reason         string `json:"reason,omitempty"` // skip 原因或失败/提示信息
	SentBytes      int64  `json:"sentBytes"`
	TotalBytes     int64  `json:"totalBytes"`

	mu sync.Mutex
}

func (n *batchNode) set(stage, reason string) {
	n.mu.Lock()
	n.Stage, n.Reason = stage, reason
	n.mu.Unlock()
}

func (n *batchNode) snapshot() (map[string]interface{}, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return map[string]interface{}{
		"username": n.Username, "goos": n.GoOS, "goarch": n.GoArch,
		"currentVersion": n.CurrentVersion, "versionId": n.VersionID, "version": n.Version,
		"role": n.Role, "stage": n.Stage, "reason": n.Reason,
		"sentBytes": n.SentBytes, "totalBytes": n.TotalBytes,
	}, n.Stage
}

func (n *batchNode) progress(sent, total int64) {
	n.mu.Lock()
	n.SentBytes, n.TotalBytes = sent, total
	n.mu.Unlock()
}

type batchTask struct {
	ID          string
	CreatedBy   string
	CreatedAt   int64
	Concurrency int
	UseCanary   bool

	mu      sync.Mutex
	phase   string
	stopped bool
	nodes   []*batchNode
}

func (t *batchTask) stoppedFlag() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func (t *batchTask) snapshot() map[string]interface{} {
	t.mu.Lock()
	phase, stopped := t.phase, t.stopped
	nodes := t.nodes
	t.mu.Unlock()

	counts := map[string]int64{"total": int64(len(nodes))}
	list := make([]map[string]interface{}, 0, len(nodes))
	for _, n := range nodes {
		snap, stage := n.snapshot()
		counts[stage]++
		list = append(list, snap)
	}
	return map[string]interface{}{
		"id": t.ID, "createdBy": t.CreatedBy, "createdAt": t.CreatedAt,
		"concurrency": t.Concurrency, "useCanary": t.UseCanary,
		"phase": phase, "stopped": stopped,
		"nodes": list, "counts": counts,
	}
}

var (
	batchTasksMu sync.Mutex
	batchTasks   = map[string]*batchTask{}
)

func putBatchTask(nt *batchTask) {
	batchTasksMu.Lock()
	defer batchTasksMu.Unlock()
	batchTasks[nt.ID] = nt
	if len(batchTasks) <= maxBatchTasks {
		return
	}
	// 先淘汰已结束的旧任务；都未结束时暂不淘汰（进行中任务不可丢）
	var victims []*batchTask
	for _, t := range batchTasks {
		t.mu.Lock()
		done := t.phase == batchPhaseDone
		t.mu.Unlock()
		if done {
			victims = append(victims, t)
		}
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].CreatedAt < victims[j].CreatedAt })
	for i := 0; i < len(batchTasks)-maxBatchTasks && i < len(victims); i++ {
		delete(batchTasks, victims[i].ID)
	}
}

func getBatchTask(id string) *batchTask {
	batchTasksMu.Lock()
	defer batchTasksMu.Unlock()
	return batchTasks[id]
}

// handleUpgradeBatchCreate POST /api/v1/clients/upgrade-batch
// 入参：{versionId 或 packages:[{versionId,goos,goarch}], usernames?, concurrency?(1-8 默认2), useCanary?(默认true)}
// 先做计划（每节点 upgrade/skip+原因），随后后台调度执行。
func (s *Server) handleUpgradeBatchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VersionID   int64    `json:"versionId"`
		Usernames   []string `json:"usernames"`
		Concurrency int      `json:"concurrency"`
		UseCanary   *bool    `json:"useCanary"`
		Packages    []struct {
			VersionID int64  `json:"versionId"`
			GoOS      string `json:"goos"`
			GoArch    string `json:"goarch"`
		} `json:"packages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求体解析失败")
		return
	}
	// 归一化平台包表：单包或多平台包二选一
	pkgs := map[string]*store.Version{} // key: goos/goarch
	if req.VersionID > 0 && len(req.Packages) == 0 {
		v, err := s.store.GetVersion(req.VersionID)
		if err != nil || v.Endpoint != "seeinps" {
			writeErr(w, http.StatusNotFound, 2004, "版本不存在")
			return
		}
		pkgs[v.GoOS+"/"+v.GoArch] = v
	}
	for _, p := range req.Packages {
		if p.GoOS == "" || p.GoArch == "" || p.VersionID <= 0 {
			writeErr(w, http.StatusBadRequest, 2000, "packages 需同时包含 versionId/goos/goarch")
			return
		}
		key := p.GoOS + "/" + p.GoArch
		if _, dup := pkgs[key]; dup {
			writeErr(w, http.StatusBadRequest, 2000, "packages 中平台 "+key+" 重复")
			return
		}
		v, err := s.store.GetVersion(p.VersionID)
		if err != nil || v.Endpoint != "seeinps" {
			writeErr(w, http.StatusNotFound, 2004, fmt.Sprintf("版本包 %d 不存在", p.VersionID))
			return
		}
		if v.GoOS != p.GoOS || v.GoArch != p.GoArch {
			writeErr(w, http.StatusBadRequest, 2000, fmt.Sprintf("版本包 %d 实际平台为 %s/%s，与声明不符", p.VersionID, v.GoOS, v.GoArch))
			return
		}
		pkgs[key] = v
	}
	if len(pkgs) == 0 {
		writeErr(w, http.StatusBadRequest, 2000, "需提供 versionId 或 packages")
		return
	}
	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = 2
	}
	if concurrency < 1 || concurrency > 8 {
		writeErr(w, http.StatusBadRequest, 2000, "并发数需在 1-8 之间")
		return
	}
	useCanary := true
	if req.UseCanary != nil {
		useCanary = *req.UseCanary
	}

	// 圈定节点范围：指定 usernames（可含离线，计划里标出）或全部在线节点
	s.clientsMu.RLock()
	all := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		all = append(all, c)
	}
	s.clientsMu.RUnlock()
	byName := map[string]*Client{}
	for _, c := range all {
		byName[c.username] = c
	}
	scope := make([]string, 0, len(all))
	seen := map[string]bool{}
	if len(req.Usernames) > 0 {
		for _, u := range req.Usernames {
			u = strings.TrimSpace(u)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			scope = append(scope, u)
		}
	} else {
		for _, c := range all {
			if _, active := s.isClientActive(c.username); active {
				scope = append(scope, c.username)
			}
		}
	}
	if len(scope) == 0 {
		writeErr(w, http.StatusBadRequest, 2000, "没有可纳入批量升级的节点")
		return
	}

	// 生成计划（每节点独立判定 skip 原因；平台字段读取与 handleClientsAPI 同样在快照后无锁进行）
	task := &batchTask{
		ID: newBatchID(), CreatedBy: operatorFrom(r), CreatedAt: time.Now().Unix(),
		Concurrency: concurrency, UseCanary: useCanary, phase: batchPhaseRunning,
	}
	sort.Strings(scope)
	for _, u := range scope {
		n := &batchNode{Username: u, Stage: "pending"}
		c, known := byName[u]
		if !known {
			n.GoOS, n.GoArch = "?", "?"
			n.set("skipped", "节点未注册或从未连接过")
			task.nodes = append(task.nodes, n)
			continue
		}
		n.GoOS, n.GoArch, n.CurrentVersion = c.goos, c.goarch, c.version
		v, hasPkg := pkgs[c.goos+"/"+c.goarch]
		_, active := s.isClientActive(u)
		switch {
		case !hasPkg:
			n.set("skipped", fmt.Sprintf("平台 %s/%s 未提供匹配版本包", c.goos, c.goarch))
		case !active:
			n.set("skipped", "节点当前不在线")
		case v.Version == c.version:
			n.set("skipped", "已是目标版本")
		case isUpgrading(u):
			n.set("skipped", "该节点正在升级中")
		default:
			n.VersionID, n.Version = v.ID, v.Version
			if compareVersions(v.Version, c.version) < 0 {
				n.Reason = "注意：目标版本低于当前版本（回滚）"
			}
		}
		task.nodes = append(task.nodes, n)
	}
	planned := 0
	for _, n := range task.nodes {
		_, stage := n.snapshot()
		if stage == "pending" {
			planned++
		}
	}
	if planned == 0 {
		writeErr(w, http.StatusBadRequest, 2000, "计划内没有任何可执行的节点（全部跳过），请检查平台匹配与节点状态")
		return
	}

	putBatchTask(task)
	detailParts := make([]string, 0, len(pkgs))
	vers := make([]string, 0, len(pkgs))
	for _, v := range pkgs {
		detailParts = append(detailParts, fmt.Sprintf("%s/%s=%s", v.GoOS, v.GoArch, v.Version))
		vers = append(vers, v.Version)
	}
	logx.Infof("[UPGRADE-BATCH] created task=%s by=%s planned=%d skipped=%d concurrency=%d canary=%v packages=[%s]",
		task.ID, task.CreatedBy, planned, len(task.nodes)-planned, concurrency, useCanary, strings.Join(detailParts, ","))
	s.audit(r, "client_upgrade_batch", task.ID,
		fmt.Sprintf("planned=%d skipped=%d concurrency=%d canary=%v packages=%s",
			planned, len(task.nodes)-planned, concurrency, useCanary, strings.Join(detailParts, ",")))

	go s.runBatchTask(task)
	writeOK(w, task.snapshot())
}

// handleUpgradeBatchList GET /api/v1/upgrade-batches：按创建时间倒序返回内存中的任务
func (s *Server) handleUpgradeBatchList(w http.ResponseWriter, r *http.Request) {
	batchTasksMu.Lock()
	tasks := make([]*batchTask, 0, len(batchTasks))
	for _, t := range batchTasks {
		tasks = append(tasks, t)
	}
	batchTasksMu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt > tasks[j].CreatedAt })
	items := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, t.snapshot())
	}
	writeOK(w, map[string]interface{}{"items": items})
}

// handleUpgradeBatchGet GET /api/v1/upgrade-batch/{id}
func (s *Server) handleUpgradeBatchGet(w http.ResponseWriter, r *http.Request) {
	t := getBatchTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, 2004, "批量任务不存在（PM 重启后内存任务已清空）")
		return
	}
	writeOK(w, t.snapshot())
}

// handleUpgradeBatchStop POST /api/v1/upgrade-batch/{id}/stop：仅停未开始的节点，
// 传输/重启中的节点不打断（掐半截没有意义）。
func (s *Server) handleUpgradeBatchStop(w http.ResponseWriter, r *http.Request) {
	t := getBatchTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, 2004, "批量任务不存在")
		return
	}
	t.mu.Lock()
	stoppedNow := false
	if !t.stopped {
		t.stopped = true
		stoppedNow = true
		for _, n := range t.nodes {
			n.mu.Lock()
			if n.Stage == "pending" {
				n.Stage, n.Reason = "skipped", "任务被手动停止"
			}
			n.mu.Unlock()
		}
	}
	t.mu.Unlock()
	if stoppedNow {
		logx.Infof("[UPGRADE-BATCH] stop requested task=%s", t.ID)
		s.audit(r, "client_upgrade_batch_stop", t.ID, "")
	}
	writeOK(w, t.snapshot())
}

// runBatchTask 调度器：金丝雀模式先升 1 台并等待终态，成功后其余按并发上限推进；
// 金丝雀失败视为目标包/链路存在系统性问题，直接跳过其余节点。
func (s *Server) runBatchTask(t *batchTask) {
	var pending []*batchNode
	for _, n := range t.nodes {
		if _, stage := n.snapshot(); stage == "pending" {
			pending = append(pending, n)
		}
	}
	start := 0
	if t.UseCanary && len(pending) > 1 {
		start = 1
		pending[0].mu.Lock()
		pending[0].Role = "canary"
		pending[0].mu.Unlock()
		s.runBatchNode(t, pending[0])
		if _, stage := pending[0].snapshot(); stage != "verified" {
			s.skipRemaining(pending[1:], "金丝雀节点升级失败，已中止批量")
			t.mu.Lock()
			t.phase = batchPhaseDone
			t.mu.Unlock()
			return
		}
		logx.Infof("[UPGRADE-BATCH] task=%s canary passed, releasing %d nodes", t.ID, len(pending)-1)
	}

	sem := make(chan struct{}, t.Concurrency)
	var wg sync.WaitGroup
	for _, n := range pending[start:] {
		if t.stoppedFlag() {
			break
		}
		if _, stage := n.snapshot(); stage != "pending" {
			continue // 停止接口可能已将其置为 skipped
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n *batchNode) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runBatchNode(t, n)
		}(n)
	}
	wg.Wait()
	t.mu.Lock()
	t.phase = batchPhaseDone
	t.mu.Unlock()
	verified, failed := 0, 0
	for _, n := range t.nodes {
		switch _, stage := n.snapshot(); stage {
		case "verified":
			verified++
		case "failed":
			failed++
		}
	}
	logx.Infof("[UPGRADE-BATCH] task=%s done verified=%d failed=%d", t.ID, verified, failed)
}

func (s *Server) skipRemaining(nodes []*batchNode, reason string) {
	for _, n := range nodes {
		n.mu.Lock()
		if n.Stage == "pending" {
			n.Stage, n.Reason = "skipped", reason
		}
		n.mu.Unlock()
	}
	if len(nodes) > 0 {
		logx.Warnf("[UPGRADE-BATCH] skipped %d remaining nodes: %s", len(nodes), reason)
	}
}

// batchNodeWaitTimeout 单节点从启动到拿到终态的上限：覆盖 30 分钟流写超时 + 重启重连窗口。
const batchNodeWaitTimeout = 40 * time.Minute

// runBatchNode 启动一个节点的升级并阻塞等待其升级状态到达终态（verified/failed）。
func (s *Server) runBatchNode(t *batchTask, n *batchNode) {
	defer func() {
		if rec := recover(); rec != nil {
			n.set("failed", fmt.Sprintf("内部错误: %v", rec))
			logx.Errorf("[UPGRADE-BATCH] task=%s node=%s panic: %v", t.ID, n.Username, rec)
		}
	}()
	n.set("running", "")
	client, active := s.isClientActive(n.Username)
	if !active {
		n.set("failed", "启动时节点已离线")
		return
	}
	v, err := s.store.GetVersion(n.VersionID)
	if err != nil {
		n.set("failed", "版本包不存在")
		return
	}
	if _, _, msg := s.startUpgradeFor(client, v); msg != "" {
		n.set("failed", msg)
		return
	}
	deadline := time.Now().Add(batchNodeWaitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		stage, _, errMsg, sent, total, _, updatedAt := getUpgradeState(n.Username)
		n.progress(sent, total)
		switch stage {
		case "verified":
			n.set("verified", "")
			logx.Infof("[UPGRADE-BATCH] task=%s node=%s verified version=%s", t.ID, n.Username, n.Version)
			return
		case "failed":
			n.set("failed", errMsg)
			return
		case "none":
			n.set("failed", "升级状态丢失（超时或状态过期）")
			return
		}
		// 已下发但节点迟迟未以新版本重连：状态 10 分钟无更新视为失败
		if stage == "sent" && time.Since(time.Unix(updatedAt, 0)) > 10*time.Minute {
			n.set("failed", "节点收包后未及时以新版本重连")
			return
		}
	}
	n.set("failed", "等待节点升级结果超时")
}

func newBatchID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().Format("01021504") + "-" + hex.EncodeToString(b)
}
