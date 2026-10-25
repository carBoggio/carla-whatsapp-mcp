# WhatsApp MCP con lista de permitidos — instalación en Azure con Tailscale

Esquema:

```
Tu ordenador (Claude Code) ──Tailscale──▶ VM de Azure
                                          ├─ tailscale serve (HTTPS 443, solo tu tailnet)
                                          │    └─▶ servidor MCP  127.0.0.1:8090  (usuario whatsapp)
                                          └─ puente WhatsApp     127.0.0.1:8080  (usuario whatsapp, no publicado)
```

La VM no tiene ningún puerto abierto a internet. Solo tus dispositivos de Tailscale llegan al MCP, y el puente nunca sale de la VM.

Qué hace el parche (sobre `lharries/whatsapp-mcp`, commit `7d6a06d`):

- **El puente aplica las listas**: solo guarda chats en `read`, solo envía a chats en `send`, bloquea el envío de archivos y las descargas de chats no permitidos. Si la lista falta o está rota, deniega todo.
- **Comandos desde tu móvil** en el chat contigo mismo (`/permitir`, `/permitir-leer`, `/quitar`, `/lista`, `/grupos`, `/ayuda`), aceptados solo desde la app del móvil principal.
- Al quitar un chat de lectura, se borra lo guardado de él. Registro de auditoría de envíos, bloqueos y cambios.
- Puente y MCP solo escuchan en `127.0.0.1` y corren como el usuario de sistema `whatsapp`.
- Se quitan `send_file`, `send_audio_message` y `download_media`.

En lo que sigue, `tu_usuario` es tu usuario de la VM, `whatsapp-vm` el nombre de la máquina y `tu-tailnet.ts.net` el dominio de tu tailnet (lo ves en el panel de Tailscale, en DNS).

---

## 1. Preparar Tailscale

En el panel de administración de Tailscale:

1. **DNS**: activa MagicDNS y los certificados HTTPS.
2. **Access controls**: sustituye la política de "permitir todo" por una que solo te deje a ti llegar a la VM:

   ```json
   {
     "tagOwners": { "tag:whatsapp": ["autogroup:admin"] },
     "grants": [
       { "src": ["tu-email@ejemplo.com"], "dst": ["tag:whatsapp"], "ip": ["tcp:443", "tcp:22"] }
     ],
     "ssh": [
       { "action": "check", "src": ["tu-email@ejemplo.com"], "dst": ["tag:whatsapp"], "users": ["tu_usuario"] }
     ]
   }
   ```

   Si ya tienes otras reglas, añade estas en lugar de reemplazarlas. `check` hace que Tailscale te pida volver a autenticarte en el navegador cada cierto tiempo para entrar por SSH.

3. **Settings → Keys**: genera una clave de autenticación **de un solo uso**, **preaprobada**, con la etiqueta `tag:whatsapp` y caducidad corta (1 hora). Las máquinas etiquetadas no caducan, así que la VM no se desconectará sola.

Nunca uses `tailscale funnel` con esta máquina: publicaría el MCP en internet.

## 2. Crear la VM en Azure

Pon la clave del paso anterior en `azure-cloud-init.yaml` (sustituye `TSKEY-AQUI`). Luego, con la CLI de Azure:

```bash
az group create -n rg-whatsapp -l <tu-región>

az vm create \
  -g rg-whatsapp -n whatsapp-vm \
  --image Ubuntu2404 \
  --size Standard_B2als_v2 \
  --admin-username tu_usuario \
  --generate-ssh-keys \
  --nsg-rule NONE \
  --custom-data azure-cloud-init.yaml
```

- `--nsg-rule NONE` deja la VM **sin ningún puerto de entrada abierto**. Tailscale solo necesita conexiones de salida.
- `Standard_B2als_v2` (2 vCPU, 4 GB) da margen para compilar. Más pequeña puede quedarse sin memoria al compilar.
- No actives el apagado automático: el puente tiene que estar encendido para recibir mensajes.
- Opcional: cifrado en el host (`--encryption-at-host true`), previo registro de la función en tu suscripción.

A los pocos minutos, la VM aparecerá en tu panel de Tailscale como `whatsapp-vm`. Entra:

```bash
ssh tu_usuario@whatsapp-vm
```

## 3. Endurecer la VM

Azure da `sudo` sin contraseña al usuario administrador. Quítalo, porque si no cualquier cosa que corra con tu usuario podría saltarse todo:

```bash
sudo passwd tu_usuario                                    # pon una contraseña
sudo rm /etc/sudoers.d/90-cloud-init-users
sudo -k && sudo true                                      # debe pedir contraseña
```

Desactiva también el SSH normal; entrarás solo por Tailscale SSH:

```bash
sudo systemctl disable --now ssh.socket ssh.service
```

Si algún día Tailscale falla, puedes entrar desde el portal de Azure con la consola serie (necesita la contraseña que acabas de poner).

Y en Azure:
- Activa **MFA** en tu cuenta y no des acceso a la suscripción a nadie más. Quien pueda gestionar la VM puede ejecutar comandos como root desde el portal o copiar el disco, y con él tus mensajes y claves de sesión.
- Ten en cuenta que las copias de seguridad o instantáneas del disco también contienen esas claves.

## 4. Herramientas de compilación (en la VM)

```bash
V=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)
curl -fsSLO "https://go.dev/dl/${V}.linux-amd64.tar.gz"
sudo tar -C /usr/local -xzf "${V}.linux-amd64.tar.gz" && rm "${V}.linux-amd64.tar.gz"
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc

curl -LsSf https://astral.sh/uv/install.sh | sh
source ~/.bashrc
go version && uv --version
```

## 5. Clonar y aplicar el parche

Copia el parche desde tu ordenador: `scp whatsapp-mcp-allowlist.patch tu_usuario@whatsapp-vm:`. En la VM:

```bash
git clone https://github.com/lharries/whatsapp-mcp.git ~/staging/whatsapp-mcp
cd ~/staging/whatsapp-mcp
git checkout 7d6a06dcdce1f01dfb24f60e1030d5efba9f3b88
git -c user.name=yo -c user.email=yo@local am ~/whatsapp-mcp-allowlist.patch
git show --stat HEAD     # revisa el diff
```

## 6. Probar y compilar

```bash
cd ~/staging/whatsapp-mcp/whatsapp-bridge
go test ./...
CGO_ENABLED=1 go build -o whatsapp-bridge .
```

## 7. Usuario aparte e instalación

```bash
sudo useradd --system --home-dir /var/lib/whatsapp --create-home --shell /usr/sbin/nologin whatsapp
sudo mkdir -p /opt/whatsapp-mcp
cd ~/staging/whatsapp-mcp
sudo cp -r whatsapp-bridge whatsapp-mcp-server /opt/whatsapp-mcp/
sudo rm -rf /opt/whatsapp-mcp/whatsapp-bridge/store
sudo install -m 755 tools/wa-allow /usr/local/bin/wa-allow

sudo env UV_PYTHON_PREFERENCE=only-system UV_CACHE_DIR=/tmp/uv-cache-wa \
  "$(command -v uv)" sync --project /opt/whatsapp-mcp/whatsapp-mcp-server --python /usr/bin/python3
sudo rm -rf /tmp/uv-cache-wa

sudo chown -R root:whatsapp /opt/whatsapp-mcp
sudo chmod -R u=rwX,g=rX,o= /opt/whatsapp-mcp
sudo install -d -o whatsapp -g whatsapp -m 700 /opt/whatsapp-mcp/whatsapp-bridge/store
```

## 8. Vincular tu móvil (una vez)

```bash
cd /tmp
sudo -u whatsapp sh -c 'cd /opt/whatsapp-mcp/whatsapp-bridge && ./whatsapp-bridge'
```

Escanea el QR que aparece en la terminal (WhatsApp → Dispositivos vinculados → Vincular dispositivo). Si se ve deformado, agranda la ventana o reduce el tamaño de letra. Cuando veas "Connected to WhatsApp", pulsa Ctrl+C.

## 9. Servicios y publicación en la tailnet

```bash
cd ~/staging/whatsapp-mcp
echo 'WA_MCP_EXTRA_HOSTS=whatsapp-vm.tu-tailnet.ts.net' | sudo tee /etc/default/whatsapp-mcp
sudo cp deploy/whatsapp-bridge.service deploy/whatsapp-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now whatsapp-bridge whatsapp-mcp

# Publica SOLO el MCP, solo en tu tailnet, por HTTPS
sudo tailscale serve --bg --https=443 http://127.0.0.1:8090
tailscale serve status
```

`WA_MCP_EXTRA_HOSTS` hace falta porque el servidor MCP rechaza peticiones con un nombre de máquina que no conoce (protección contra ataques de DNS rebinding).

## 10. Configurar las listas desde el móvil

En el chat contigo mismo, escribe `/ayuda`. Responderá con 🔒. Ejemplos:

```
/permitir 34600111222          → la IA puede escribirle
/permitir-leer 34600111222     → la IA puede leer ese chat
/grupos                        → lista tus grupos con su ID
/permitir-leer 1203630...@g.us → leer un grupo
/quitar 34600111222            → quita todo y borra lo guardado
/lista
```

Los números van con código de país. Al permitir la lectura de un chat, se guarda desde ese momento; el historial anterior no se recupera.

Alternativa por SSH en la VM: `sudo wa-allow list`, `sudo wa-allow add send 34600111222`, `sudo wa-allow remove all 34600111222`.

## 11. Claude Code en tu ordenador

```bash
claude mcp add --transport http --scope user whatsapp https://whatsapp-vm.tu-tailnet.ts.net/mcp
```

Configuración global (requiere permisos de administrador en tu ordenador): copia `deploy/managed-settings.json` a

- Linux: `/etc/claude-code/managed-settings.json`
- macOS: `/Library/Application Support/ClaudeCode/managed-settings.json`
- Windows: `C:\Program Files\ClaudeCode\managed-settings.json`

Hace que Claude Code te pida confirmación antes de cada mensaje de WhatsApp y le impide usar `ssh`, `scp` o `tailscale`, para que no entre por su cuenta en la VM. Es una capa extra: lo que de verdad protege son las listas en el puente, el usuario `whatsapp` y que tu `sudo` en la VM pida contraseña.

## 12. Comprobaciones

Desde tu ordenador:

```bash
# El MCP responde por la tailnet
curl -s -o /dev/null -w '%{http_code}\n' https://whatsapp-vm.tu-tailnet.ts.net/mcp     # 405 o 406 está bien; 421 = revisa WA_MCP_EXTRA_HOSTS

# El puente NO es accesible desde fuera de la VM
curl -m 5 http://whatsapp-vm:8080/api/send                                             # debe fallar
```

En la VM (por SSH):

```bash
ls /opt/whatsapp-mcp/whatsapp-bridge/store                  # → Permission denied

curl -s -X POST http://127.0.0.1:8080/api/send \
  -d '{"recipient":"34699999999","message":"prueba"}'       # → Recipient not allowed

sudo tail /opt/whatsapp-mcp/whatsapp-bridge/store/audit.log
```

Además:
- Escribe `/lista` desde **WhatsApp Web** en el chat contigo mismo: no debe responder.
- Pide a Claude Code que liste tus chats: solo deben salir los permitidos.
- Pídele que escriba a un número no permitido: debe fallar.
- Desde un dispositivo de la tailnet que no seas tú (si tienes alguno compartido), el MCP no debe responder.

## Uso diario

- Logs: `journalctl -u whatsapp-bridge -f`
- WhatsApp pide volver a vincular cada ~20 días: `sudo systemctl stop whatsapp-mcp whatsapp-bridge`, repite el paso 8 y vuelve a arrancarlos.
- Actualizaciones del sistema: `sudo apt update && sudo apt upgrade`, o activa `unattended-upgrades`.
- Actualizar el puente: recompila en `~/staging`, pasa los tests, `sudo install -o root -g whatsapp -m 750 whatsapp-bridge /opt/whatsapp-mcp/whatsapp-bridge/` y reinicia.

## Limitaciones conocidas

- El proyecto original usa una versión de whatsmeow de marzo de 2025. Si WhatsApp la rechaza por desactualizada, habrá que actualizarla (`go get go.mau.fi/whatsmeow@latest`) y ajustar pequeños cambios de API; vuelve a pasar los tests.
- Si WhatsApp identifica un chat con un ID `@lid` en lugar del número, el puente lo trata como no permitido (falla cerrado).
- Conectarse desde una IP de centro de datos llama algo más la atención de WhatsApp que hacerlo desde casa.
- Quien tenga tu móvil desbloqueado puede cambiar las listas.
- Quien administre tu suscripción de Azure o tu tailnet tiene acceso a la máquina.
