package handler

import "cyberstrike-ai/internal/store"

func publicWebshellConnection(conn *store.WebShellConnection) *store.WebShellConnection {
	if conn == nil {
		return nil
	}
	public := *conn
	if public.Password != "" {
		public.Password = maskedSecret
	}
	return &public
}
