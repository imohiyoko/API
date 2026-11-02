// Package users は、1つの定義から internal・partner・public の API を作る例。
package users

import (
	"context"
	"sync"

	"github.com/imohiyoko/API/define"
)

// User はユーザー。email は partner 以上、note は internal だけに見せる。
type User struct {
	ID    string `api:"id"`
	Name  string `api:"name"`
	Email string `api:"email,min=partner"`
	Note  string `api:"note,min=internal"`
}

type GetUserIn struct {
	ID string `api:"id,path"`
}

type ListUsersIn struct {
	Limit *int `api:"limit"`
}

type CreateUserIn struct {
	Name  string `api:"name"`
	Email string `api:"email"`
	Note  string `api:"note,min=internal,optional"`
}

type DeleteUserIn struct {
	ID string `api:"id,path"`
}

type Deleted struct{}

// Store はインメモリのユーザーの保存先。
type Store struct {
	mu    sync.Mutex
	users []User
}

// Registry は操作の定義を返す。
func Registry(s *Store) *define.Registry {
	get := define.Query[GetUserIn, User]("users.get",
		define.Doc("ユーザーを取得する"),
		define.Authz("users:read", define.Resource("user", "id")),
		define.Expose(define.Internal, define.REST, define.GRPC),
		define.Expose(define.Partner, define.REST),
		define.Expose(define.Public, define.REST),
	).Handle(func(_ context.Context, in GetUserIn) (User, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, u := range s.users {
			if u.ID == in.ID {
				return u, nil
			}
		}
		return User{}, define.Errorf(define.CodeNotFound, "user %s", in.ID)
	})

	list := define.Query[ListUsersIn, []User]("users.list",
		define.Doc("ユーザーの一覧"),
		define.Authz("users:list"),
		define.Expose(define.Internal, define.REST),
		define.Expose(define.Partner, define.REST),
	).Handle(func(_ context.Context, in ListUsersIn) ([]User, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := len(s.users)
		if in.Limit != nil && *in.Limit < n {
			n = max(*in.Limit, 0)
		}
		return append([]User(nil), s.users[:n]...), nil
	})

	create := define.Command[CreateUserIn, User]("users.create",
		define.Doc("ユーザーを作る"),
		define.Authz("users:create"),
		define.Expose(define.Internal, define.REST),
	).Handle(func(_ context.Context, in CreateUserIn) (User, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		u := User{ID: "usr_" + string(rune('a'+len(s.users))), Name: in.Name, Email: in.Email, Note: in.Note}
		s.users = append(s.users, u)
		return u, nil
	})

	del := define.Command[DeleteUserIn, Deleted]("users.delete",
		define.Doc("ユーザーを消す"),
		define.Authz("users:delete", define.Resource("user", "id")),
		define.Expose(define.Internal, define.REST),
	).Handle(func(_ context.Context, in DeleteUserIn) (Deleted, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, u := range s.users {
			if u.ID == in.ID {
				s.users = append(s.users[:i], s.users[i+1:]...)
				return Deleted{}, nil
			}
		}
		return Deleted{}, define.Errorf(define.CodeNotFound, "user %s", in.ID)
	})

	return define.NewRegistry().Add(get, list, create, del)
}
