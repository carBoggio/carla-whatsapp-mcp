# WhatsApp MCP con lista de permitidos — instalación

Parche sobre `lharries/whatsapp-mcp` (commit `7d6a06d`). Qué cambia:

- **El puente aplica las listas**: solo guarda mensajes de chats en `read`, solo envía a chats en `send`, bloquea el envío de archivos y las descargas de chats no permitidos. Si la lista falta o está rota, deniega todo.
- **Comandos desde tu móvil** en el chat contigo mismo (`/permitir`, `/permitir-leer`, `/quitar`, `/lista`, `/grupos`, `/ayuda`). Solo se aceptan desde la app del móvil principal; desde WhatsApp Web, Desktop o el propio puente se ignoran.
- Al quitar un chat de lectura, se borra lo que había guardado de él.
- Registro de auditoría de cada envío, bloqueo y cambio de lista.
- El puente solo escucha en `127.0.0.1`. El servidor MCP corre por HTTP (`127.0.0.1:8090`) para que ambos funcionen como un usuario del sistema aparte, `whatsapp`.
- Se quitan las herramientas `send_file`, `send_audio_message` y `download_media`.

Los comandos con `sudo` los ejecutas tú, no la IA.

## 1. Requisitos

Go 1.24 o superior, `gcc` (para SQLite), Python 3.11 o superior, `git` y `uv`.

## 2. Clonar y aplicar el parche

```bash
git clone https://github.com/lharries/whatsapp-mcp.git ~/staging/whatsapp-mcp
cd ~/staging/whatsapp-mcp
git checkout 7d6a06dcdce1f01dfb24f60e1030d5efba9f3b88
git am /ruta/a/whatsapp-mcp-allowlist.patch
```

Revisa el diff antes de seguir: `git show --stat HEAD` y `git show HEAD`.

## 3. Probar y compilar

```bash
cd ~/staging/whatsapp-mcp/whatsapp-bridge
go test ./...
CGO_ENABLED=1 go build -o whatsapp-bridge .
```

## 4. Usuario aparte e instalación

```bash
sudo useradd --system --home-dir /var/lib/whatsapp --create-home --shell /usr/sbin/nologin whatsapp
sudo mkdir -p /opt/whatsapp-mcp
cd ~/staging/whatsapp-mcp
sudo cp -r whatsapp-bridge whatsapp-mcp-server /opt/whatsapp-mcp/
sudo rm -rf /opt/whatsapp-mcp/whatsapp-bridge/store   # empezar sin datos previos
sudo install -m 755 tools/wa-allow /usr/local/bin/wa-allow

# Entorno de Python con el Python del sistema (legible por el usuario whatsapp)
sudo env UV_PYTHON_PREFERENCE=only-system UV_CACHE_DIR=/tmp/uv-cache-wa \
  "$(command -v uv)" sync --project /opt/whatsapp-mcp/whatsapp-mcp-server --python /usr/bin/python3
sudo rm -rf /tmp/uv-cache-wa

# Permisos: el código es de root (ni la IA ni el propio servicio pueden modificarlo)
sudo chown -R root:whatsapp /opt/whatsapp-mcp
sudo chmod -R u=rwX,g=rX,o= /opt/whatsapp-mcp
sudo install -d -o whatsapp -g whatsapp -m 700 /opt/whatsapp-mcp/whatsapp-bridge/store
```

## 5. Vincular tu móvil (una vez)

```bash
cd /tmp
sudo -u whatsapp sh -c 'cd /opt/whatsapp-mcp/whatsapp-bridge && ./whatsapp-bridge'
```

Escanea el QR en WhatsApp → Dispositivos vinculados → Vincular dispositivo. Cuando veas "Connected to WhatsApp", pulsa Ctrl+C.

## 6. Servicios

```bash
cd ~/staging/whatsapp-mcp
sudo cp deploy/whatsapp-bridge.service deploy/whatsapp-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now whatsapp-bridge whatsapp-mcp
systemctl status whatsapp-bridge whatsapp-mcp
```

## 7. Configurar las listas desde el móvil

Abre en tu móvil el chat contigo mismo y escribe `/ayuda`. Responderá con 🔒. Ejemplos:

```
/permitir 34600111222          → la IA puede escribirle
/permitir-leer 34600111222     → la IA puede leer ese chat
/grupos                        → lista tus grupos con su ID
/permitir-leer 1203630...@g.us → leer un grupo
/quitar 34600111222            → quita todo y borra lo guardado
/lista
```

Los números van con código de país. Al permitir la lectura de un chat, se guarda desde ese momento; el historial anterior no se recupera.

Alternativa desde la terminal: `sudo wa-allow list`, `sudo wa-allow add send 34600111222`, `sudo wa-allow remove all 34600111222`.

## 8. Claude Code (global)

```bash
sudo mkdir -p /etc/claude-code
sudo install -o root -g root -m 644 deploy/managed-settings.json /etc/claude-code/managed-settings.json
claude mcp add --transport http --scope user whatsapp http://127.0.0.1:8090/mcp
```

Esto hace que Claude Code te pida confirmación antes de cada envío y le impide leer o editar `/opt/whatsapp-mcp`, la configuración gestionada y los servicios. Es una capa extra: la protección real es el usuario `whatsapp` y las listas en el puente.

## 9. Comprobaciones

Hazlas tú desde tu usuario normal:

```bash
# 1. Tu usuario no puede leer los datos ni las claves
ls /opt/whatsapp-mcp/whatsapp-bridge/store                 # → Permission denied

# 2. Saltarse el MCP llamando al puente sigue bloqueado
curl -s -X POST http://127.0.0.1:8080/api/send \
  -d '{"recipient":"34699999999","message":"prueba"}'      # → Recipient not allowed

# 3. Registro de auditoría
sudo tail /opt/whatsapp-mcp/whatsapp-bridge/store/audit.log
```

Además:
- Escribe `/lista` desde **WhatsApp Web** en el chat contigo mismo: no debe responder.
- Pide a Claude Code que liste tus chats: solo deben salir los permitidos.
- Pídele que escriba a un número no permitido: debe fallar.

## Uso diario

- Logs: `journalctl -u whatsapp-bridge -f`
- WhatsApp pide volver a vincular cada ~20 días: `sudo systemctl stop whatsapp-mcp whatsapp-bridge`, repite el paso 5 y vuelve a arrancarlos.
- Actualizar: recompila en `~/staging`, pasa los tests, copia el binario con `sudo install -o root -g whatsapp -m 750 whatsapp-bridge /opt/whatsapp-mcp/whatsapp-bridge/` y reinicia.

## Limitaciones conocidas

- El proyecto original usa una versión de whatsmeow de marzo de 2025. Si WhatsApp la rechaza por desactualizada, habrá que actualizarla (`go get go.mau.fi/whatsmeow@latest`) y ajustar pequeños cambios de API; vuelve a pasar los tests.
- Si WhatsApp identifica un chat con un ID `@lid` en lugar del número, el puente lo trata como no permitido (falla cerrado).
- Quien tenga tu móvil desbloqueado puede cambiar las listas.
- Tu usuario no debe tener `sudo` sin contraseña; si lo tiene, la IA podría saltarse todo esto.
