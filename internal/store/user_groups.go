package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UserGroup 用户分组树节点。系统有且仅有一个根分组（is_root=1）：不可删除、可改名，
// 全部用户与分组都在其之下；同级分组内名称唯一（ux_user_groups_parent_name）。
type UserGroup struct {
	ID        int64
	ParentID  int64
	Name      string
	IsRoot    bool
	CreatedAt int64
	UpdatedAt int64
}

// MaxUserGroupDepth 分组树最大层数（根分组计第 1 层，即根下最多再建 4 层）
const MaxUserGroupDepth = 5

// ErrRootGroupUndeletable 根分组不允许删除
var ErrRootGroupUndeletable = errors.New("根分组不可删除")

// RootGroupID 返回根分组 id（migrate 已确保存在，运行期兜底再建一次）
func (s *Store) RootGroupID() (int64, error) {
	return s.ensureRootGroup()
}

// UserGroupStat 分组及其统计（直属用户数 / 直属子分组数），供管理端列表与删除前置判断展示
type UserGroupStat struct {
	UserGroup
	UserCount  int
	ChildCount int
}

// ListUserGroups 返回全部分组（平铺，前端按 parent_id 组树），并带直属用户数与直属子分组数
func (s *Store) ListUserGroups() ([]*UserGroupStat, error) {
	rows, err := s.db.Query(`SELECT g.id, g.parent_id, g.name, g.is_root, g.created_at, g.updated_at,
		(SELECT COUNT(*) FROM users u WHERE u.group_id = g.id),
		(SELECT COUNT(*) FROM user_groups c WHERE c.parent_id = g.id)
		FROM user_groups g ORDER BY g.parent_id, g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []*UserGroupStat
	for rows.Next() {
		g := &UserGroupStat{}
		var isRoot int
		if err := rows.Scan(&g.ID, &g.ParentID, &g.Name, &isRoot, &g.CreatedAt, &g.UpdatedAt, &g.UserCount, &g.ChildCount); err != nil {
			return nil, err
		}
		g.IsRoot = isRoot == 1
		groups = append(groups, g)
	}
	return groups, nil
}

// GetUserGroup 按 id 取分组，不存在返回 sql.ErrNoRows
func (s *Store) GetUserGroup(id int64) (*UserGroup, error) {
	g := &UserGroup{}
	var isRoot int
	err := s.db.QueryRow("SELECT id, parent_id, name, is_root, created_at, updated_at FROM user_groups WHERE id = ?", id).
		Scan(&g.ID, &g.ParentID, &g.Name, &isRoot, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	g.IsRoot = isRoot == 1
	return g, nil
}

// GroupDepth 沿 parent 链上溯计算分组所在层级（根=1）；异常数据成环时报错防死循环
func (s *Store) GroupDepth(id int64) (*UserGroup, int, error) {
	depth := 0
	cur := id
	for seen := map[int64]bool{}; ; {
		if seen[cur] {
			return nil, 0, fmt.Errorf("分组数据异常：父子链存在环")
		}
		seen[cur] = true
		g, err := s.GetUserGroup(cur)
		if err != nil {
			return nil, 0, err
		}
		depth++
		if g.IsRoot || g.ParentID == 0 {
			return g, depth, nil
		}
		cur = g.ParentID
	}
}

// CreateUserGroup 在 parentID 下新建分组；父分组不存在 / 超 5 层 / 同级重名时返回错误
func (s *Store) CreateUserGroup(parentID int64, name string) (*UserGroup, error) {
	parent, err := s.GetUserGroup(parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("父分组不存在")
		}
		return nil, err
	}
	_, depth, err := s.GroupDepth(parentID)
	if err != nil {
		return nil, err
	}
	if depth >= MaxUserGroupDepth {
		return nil, fmt.Errorf("分组最多 %d 层，「%s」已在最后一层，无法再建下级", MaxUserGroupDepth, parent.Name)
	}
	now := time.Now().Unix()
	res, err := s.db.Exec("INSERT INTO user_groups (parent_id, name, is_root, created_at, updated_at) VALUES (?, ?, 0, ?, ?)", parentID, name, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, fmt.Errorf("同级下已存在同名分组")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &UserGroup{ID: id, ParentID: parentID, Name: name, CreatedAt: now, UpdatedAt: now}, nil
}

// RenameUserGroup 重命名分组（根分组也允许改名）；同级重名时返回错误
func (s *Store) RenameUserGroup(id int64, name string) error {
	g, err := s.GetUserGroup(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("分组不存在")
		}
		return err
	}
	if _, err := s.db.Exec("UPDATE user_groups SET name = ?, updated_at = ? WHERE id = ?", name, time.Now().Unix(), g.ID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return fmt.Errorf("同级下已存在同名分组")
		}
		return err
	}
	return nil
}

// DeleteUserGroup 删除分组：根分组不可删；仍有子分组或直属用户时禁止删除（需先移走）
func (s *Store) DeleteUserGroup(id int64) error {
	g, err := s.GetUserGroup(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("分组不存在")
		}
		return err
	}
	if g.IsRoot {
		return ErrRootGroupUndeletable
	}
	var children, users int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM user_groups WHERE parent_id = ?", id).Scan(&children); err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("该分组下还有 %d 个子分组，请先删除或移走子分组", children)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE group_id = ?", id).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return fmt.Errorf("该分组下还有 %d 个用户，请先将其移动到其它分组", users)
	}
	_, err = s.db.Exec("DELETE FROM user_groups WHERE id = ?", id)
	return err
}

// SetUsersGroup 批量把用户移动到指定分组，返回实际更新的人数；分组必须存在
func (s *Store) SetUsersGroup(usernames []string, groupID int64) (int, error) {
	if len(usernames) == 0 {
		return 0, nil
	}
	if _, err := s.GetUserGroup(groupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("分组不存在")
		}
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("UPDATE users SET group_id = ?, updated_at = ? WHERE username = ?")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	moved := 0
	for _, name := range usernames {
		res, err := stmt.Exec(groupID, now, name)
		if err != nil {
			return moved, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			moved++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return moved, nil
}
