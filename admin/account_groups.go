package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

const (
	maxAccountGroups            = 64
	maxAccountGroupNameRuneSize = 80
	autoAccountGroupFree        = "FREE"
	autoAccountGroupAPI         = "API"
)

type accountGroupResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SortOrder   int64  `json:"sort_order"`
	MemberCount int64  `json:"member_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toAccountGroupResponse(g database.AccountGroup) accountGroupResponse {
	return accountGroupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		Color:       g.Color,
		SortOrder:   g.SortOrder,
		MemberCount: g.MemberCount,
		CreatedAt:   g.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   g.UpdatedAt.Format(time.RFC3339),
	}
}

func (h *Handler) ListAccountGroups(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	groups, err := h.db.ListAccountGroups(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	out := make([]accountGroupResponse, 0, len(groups))
	for _, group := range groups {
		out = append(out, toAccountGroupResponse(group))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

type createAccountGroupReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SortOrder   *int64 `json:"sort_order"`
}

func (h *Handler) CreateAccountGroup(c *gin.Context) {
	var req createAccountGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, err := sanitizeAccountGroupName(req.Name)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if utf8.RuneCountInString(description) > 240 {
		writeError(c, http.StatusBadRequest, "描述长度不能超过 240 字符")
		return
	}
	color := strings.TrimSpace(req.Color)
	if utf8.RuneCountInString(color) > 20 {
		writeError(c, http.StatusBadRequest, "颜色长度不能超过 20 字符")
		return
	}
	sortOrder := int64(0)
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	groups, err := h.db.ListAccountGroups(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if len(groups) >= maxAccountGroups {
		writeError(c, http.StatusBadRequest, "分组数量已达上限")
		return
	}
	id, err := h.db.CreateAccountGroup(ctx, name, description, color, sortOrder)
	if err != nil {
		if errors.Is(err, database.ErrDuplicateAccountGroupName) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "message": "分组已创建"})
}

type updateAccountGroupReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
	SortOrder   *int64  `json:"sort_order"`
}

func (h *Handler) UpdateAccountGroup(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的分组 ID")
		return
	}
	var req updateAccountGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Name != nil {
		name, err := sanitizeAccountGroupName(*req.Name)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
		req.Name = &name
	}
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(desc) > 240 {
			writeError(c, http.StatusBadRequest, "描述长度不能超过 240 字符")
			return
		}
		req.Description = &desc
	}
	if req.Color != nil {
		color := strings.TrimSpace(*req.Color)
		if utf8.RuneCountInString(color) > 20 {
			writeError(c, http.StatusBadRequest, "颜色长度不能超过 20 字符")
			return
		}
		req.Color = &color
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.db.UpdateAccountGroup(ctx, id, req.Name, req.Description, req.Color, req.SortOrder); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(c, http.StatusNotFound, "分组不存在")
			return
		}
		if errors.Is(err, database.ErrDuplicateAccountGroupName) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}
		writeInternalError(c, err)
		return
	}
	writeMessage(c, http.StatusOK, "分组已更新")
}

func (h *Handler) DeleteAccountGroup(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的分组 ID")
		return
	}
	force := strings.EqualFold(c.Query("force"), "true")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.db.DeleteAccountGroup(ctx, id, force); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(c, http.StatusNotFound, "分组不存在")
			return
		}
		if errors.Is(err, database.ErrAccountGroupNotEmpty) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}
		writeInternalError(c, err)
		return
	}
	if h.store != nil {
		for _, acc := range h.store.Accounts() {
			acc.Mu().RLock()
			groups := removeInt64(acc.GroupIDs, id)
			acc.Mu().RUnlock()
			h.store.ApplyAccountGroups(acc.DBID, groups)
		}
	}
	h.refreshAPIKeyAllowedGroupsAfterGroupDelete(ctx, id)
	writeMessage(c, http.StatusOK, "分组已删除")
}

func (h *Handler) refreshAPIKeyAllowedGroupsAfterGroupDelete(ctx context.Context, groupID int64) {
	if h == nil || h.db == nil || groupID <= 0 {
		return
	}
	keys, err := h.db.ListAPIKeys(ctx)
	if err != nil {
		return
	}
	for _, key := range keys {
		if key == nil {
			continue
		}
		if h.store != nil {
			h.store.SetAPIKeyAllowedGroups(key.ID, key.AllowedGroupIDs)
		}
		h.invalidateAPIKeyRuntimeCaches(ctx, key.Key)
	}
}

func sanitizeAccountGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("分组名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxAccountGroupNameRuneSize {
		return "", errors.New("分组名称长度超过 80 字符")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("分组名称包含非法控制字符")
		}
	}
	return name, nil
}

func removeInt64(slice []int64, target int64) []int64 {
	out := make([]int64, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

func containsInt64(slice []int64, target int64) bool {
	for _, v := range slice {
		if v == target {
			return true
		}
	}
	return false
}

func dedupeInt64(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (h *Handler) applyAccountCreateMetadata(ctx context.Context, accountID int64, tags optionalStringSlice, autoGroupName string) ([]int64, error) {
	if h == nil || h.db == nil || accountID <= 0 {
		return nil, nil
	}
	if tags.Set {
		if err := h.db.UpdateAccountTags(ctx, accountID, tags.Values); err != nil {
			return nil, err
		}
		if h.store != nil {
			h.store.ApplyAccountTags(accountID, tags.Values)
		}
	}
	if strings.TrimSpace(autoGroupName) == "" {
		return h.db.GetAccountGroupIDs(ctx, accountID)
	}
	groupID, err := h.ensureAccountGroup(ctx, autoGroupName)
	if err != nil {
		return nil, err
	}
	groupIDs, err := h.db.GetAccountGroupIDs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !containsInt64(groupIDs, groupID) {
		groupIDs = append(groupIDs, groupID)
		groupIDs = dedupeInt64(groupIDs)
		if err := h.db.SetAccountGroups(ctx, accountID, groupIDs); err != nil {
			return nil, err
		}
	}
	if h.store != nil {
		h.store.ApplyAccountGroups(accountID, groupIDs)
	}
	return groupIDs, nil
}

func (h *Handler) ensureAccountGroup(ctx context.Context, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	groups, err := h.db.ListAccountGroups(ctx)
	if err != nil {
		return 0, err
	}
	for _, group := range groups {
		if group.Name == name {
			return group.ID, nil
		}
	}
	id, err := h.db.CreateAccountGroup(ctx, name, autoAccountGroupDescription(name), autoAccountGroupColor(name), autoAccountGroupSortOrder(name))
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, database.ErrDuplicateAccountGroupName) {
		return 0, err
	}
	groups, err = h.db.ListAccountGroups(ctx)
	if err != nil {
		return 0, err
	}
	for _, group := range groups {
		if group.Name == name {
			return group.ID, nil
		}
	}
	return 0, err
}

func autoAccountGroupForPlan(planType string) string {
	if strings.EqualFold(strings.TrimSpace(planType), "free") {
		return autoAccountGroupFree
	}
	return ""
}

func autoAccountGroupDescription(name string) string {
	switch name {
	case autoAccountGroupFree:
		return "自动归类的 Free 账号"
	case autoAccountGroupAPI:
		return "自动归类的 API Key 账号"
	default:
		return ""
	}
}

func autoAccountGroupColor(name string) string {
	switch name {
	case autoAccountGroupFree:
		return "#16a34a"
	case autoAccountGroupAPI:
		return "#2563eb"
	default:
		return "#64748b"
	}
}

func autoAccountGroupSortOrder(name string) int64 {
	switch name {
	case autoAccountGroupAPI:
		return 10
	case autoAccountGroupFree:
		return 20
	default:
		return 100
	}
}
