package main

// Owner commands sent from YOUR PHONE to the "message yourself" chat.
//
//   /permitir <número|grupo@g.us>       -> puede ENVIAR a ese chat
//   /permitir-leer <número|grupo@g.us>  -> puede LEER ese chat
//   /quitar <número|grupo@g.us>         -> quita ambos permisos y borra lo guardado
//   /quitar-envio <número|grupo@g.us>   -> quita solo el permiso de envío
//   /quitar-leer <número|grupo@g.us>    -> quita solo la lectura y borra lo guardado
//   /lista                              -> muestra ambas listas
//   /grupos                             -> lista tus grupos con su JID (para copiarlo)
//   /ayuda
//
// A command is accepted ONLY if all of these hold:
//   - the message was sent by this account (IsFromMe),
//   - it comes from the PRIMARY phone (device 0), never from a linked device
//     such as this bridge, WhatsApp Web or WhatsApp Desktop,
//   - it is in the chat with yourself.
// Anything else is ignored. Command messages are never stored.

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const replyPrefix = "🔒 "

// isOwnerCommand decides whether a message is a trusted command from the owner's phone.
func isOwnerCommand(own *types.JID, info types.MessageInfo, text string) bool {
	if own == nil || !info.IsFromMe {
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "/") {
		return false
	}
	sender := info.Sender
	if sender.Server != types.DefaultUserServer || sender.User != own.User || sender.Device != 0 {
		return false
	}
	chat := info.Chat
	return chat.Server == types.DefaultUserServer && chat.User == own.User
}

// isSelfChat reports whether a message belongs to the chat with yourself
// (whether or not it is a command). Those messages are never stored.
func isSelfChat(own *types.JID, chat types.JID) bool {
	return own != nil && chat.Server == types.DefaultUserServer && chat.User == own.User
}

type commandEnv struct {
	allow      *Allowlist
	purgeChat  func(key string) error
	listGroups func() (string, error)
	audit      *AuditLog
}

// runCommand executes one command and returns the reply text.
func runCommand(env commandEnv, text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "Comando vacío. Escribe /ayuda."
	}
	cmd := strings.ToLower(fields[0])
	arg := strings.Join(fields[1:], " ")

	needArg := func() (string, bool) {
		if arg == "" {
			return "Falta el número o grupo. Ej: " + cmd + " 34600111222", false
		}
		return "", true
	}

	switch cmd {
	case "/ayuda", "/help":
		return "Comandos:\n" +
			"/permitir N — puede enviar a N\n" +
			"/permitir-leer N — puede leer N\n" +
			"/quitar N — quita todo y borra lo guardado\n" +
			"/quitar-envio N — quita solo el envío\n" +
			"/quitar-leer N — quita la lectura y borra lo guardado\n" +
			"/lista — muestra las listas\n" +
			"/grupos — tus grupos con su ID\n" +
			"N = número con código de país (34600111222) o ID de grupo (...@g.us)"

	case "/lista":
		send, read := env.allow.Snapshot()
		return fmt.Sprintf("Puede ENVIAR a (%d):\n%s\n\nPuede LEER (%d):\n%s",
			len(send), listOrNone(send), len(read), listOrNone(read))

	case "/grupos":
		if env.listGroups == nil {
			return "No disponible."
		}
		out, err := env.listGroups()
		if err != nil {
			return "Error obteniendo grupos: " + err.Error()
		}
		return out

	case "/permitir", "/permitir-leer":
		if msg, ok := needArg(); !ok {
			return msg
		}
		kind, label := "send", "ENVIAR a"
		if cmd == "/permitir-leer" {
			kind, label = "read", "LEER"
		}
		k, err := env.allow.Update(kind, arg, true)
		if err != nil {
			return "No se pudo añadir: " + err.Error()
		}
		env.audit.Printf("PHONE add %s %s", kind, k)
		extra := ""
		if kind == "read" {
			extra = "\nSe guardarán sus mensajes desde ahora (el historial anterior no se recupera automáticamente)."
		}
		return fmt.Sprintf("✅ Ahora la IA puede %s %s.%s", label, k, extra)

	case "/quitar", "/quitar-envio", "/quitar-leer":
		if msg, ok := needArg(); !ok {
			return msg
		}
		var kinds []string
		switch cmd {
		case "/quitar":
			kinds = []string{"send", "read"}
		case "/quitar-envio":
			kinds = []string{"send"}
		default:
			kinds = []string{"read"}
		}
		var k string
		for _, kind := range kinds {
			var err error
			k, err = env.allow.Update(kind, arg, false)
			if err != nil {
				return "No se pudo quitar: " + err.Error()
			}
			env.audit.Printf("PHONE remove %s %s", kind, k)
		}
		purged := ""
		if cmd != "/quitar-envio" && env.purgeChat != nil {
			if err := env.purgeChat(k); err != nil {
				purged = "\n⚠️ Error borrando mensajes guardados: " + err.Error()
			} else {
				purged = "\nMensajes guardados de ese chat borrados."
			}
		}
		return fmt.Sprintf("🚫 Permisos quitados para %s (%s).%s", k, strings.Join(kinds, " y "), purged)
	}
	return "Comando desconocido. Escribe /ayuda."
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "  (ninguno)"
	}
	return "  " + strings.Join(items, "\n  ")
}

// handleOwnerCommand runs the command and replies in the self chat.
func handleOwnerCommand(client *whatsmeow.Client, store *MessageStore, msg *events.Message, text string) {
	env := commandEnv{
		allow: allow,
		audit: audit,
		purgeChat: func(key string) error {
			return store.PurgeChat(key)
		},
		listGroups: func() (string, error) {
			groups, err := client.GetJoinedGroups()
			if err != nil {
				return "", err
			}
			if len(groups) == 0 {
				return "No estás en ningún grupo.", nil
			}
			var b strings.Builder
			b.WriteString("Tus grupos (copia el ID para /permitir-leer):\n")
			for _, g := range groups {
				fmt.Fprintf(&b, "• %s\n  %s\n", g.Name, g.JID.String())
			}
			return b.String(), nil
		},
	}
	reply := runCommand(env, text)
	own := client.Store.ID.ToNonAD()
	_, err := client.SendMessage(context.Background(), own, &waProto.Message{
		Conversation: proto.String(replyPrefix + reply),
	})
	if err != nil {
		fmt.Printf("[commands] no se pudo responder: %v\n", err)
	}
}
