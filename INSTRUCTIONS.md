# Instrucciones para Claude Code

Este repositorio es `lharries/whatsapp-mcp` con dos commits añadidos encima del original (`7d6a06d`):

1. **Allowlist hardening**: el puente Go solo guarda los chats de la lista `read`, solo envía a los de la lista `send`, bloquea archivos y descargas, y acepta cambios de lista solo desde el móvil del dueño (`/permitir`, `/permitir-leer`, `/quitar`, `/lista`, `/grupos`) en el chat consigo mismo.
2. **Azure + Tailscale**: despliegue en una VM de Azure sin puertos de entrada, con el MCP publicado solo en la tailnet del usuario.

El objetivo es que el dueño use WhatsApp personal desde Claude Code con acceso limitado a chats concretos. La guía completa para el humano está en `deploy/INSTALAR.md`. Léela entera antes de empezar.

## Reglas (no negociables)

- **Nunca ejecutes `sudo`**, ni en este ordenador ni en la VM. Cuando un paso lo requiera, dale al usuario el comando exacto y espera a que te diga que lo ejecutó.
- **Nunca entres por `ssh`/`scp`/`tailscale ssh` a la VM.** Los pasos en la VM los ejecuta el usuario; tú le das los comandos.
- **No debilites ninguna protección** para que algo funcione: ni las listas del puente, ni que escuche solo en `127.0.0.1`, ni los permisos de `/opt/whatsapp-mcp`, ni la regla de "falla cerrado". Si algo choca, explícalo y que decida el usuario.
- **Ningún secreto ni dato personal en el repositorio**: números de teléfono, la clave de Tailscale, el email del usuario o el nombre de su tailnet. La clave de Tailscale va en una copia de `deploy/azure-cloud-init.yaml` fuera del repo.
- **Pide confirmación explícita** antes de: crear o publicar un repositorio en GitHub, hacer `git push`, y crear cualquier recurso en Azure (`az ... create`), porque cuesta dinero.
- Nunca uses `tailscale funnel`: publicaría el MCP en internet.

## Tarea 1 — Revisar

1. `git log --oneline -3` y `git show --stat HEAD~1 HEAD` para ver los cambios.
2. Ejecuta los tests del puente: `cd whatsapp-bridge && go test ./...` (necesita Go 1.24+ y `gcc`; si faltan, díselo al usuario).
3. Resume al usuario qué hace cada cambio y si los tests pasan. Para aquí y espera.

## Tarea 2 — Publicar en GitHub

Pregunta al usuario cuál prefiere:

- **Fork público** (un fork de un repo público siempre es público):
  ```bash
  gh repo fork --remote=true      # origin pasa a ser el fork; el original queda como upstream
  git push origin HEAD:main
  ```
- **Repo privado nuevo**:
  ```bash
  git remote rename origin upstream
  gh repo create <nombre> --private --source=. --remote=origin --push
  ```

Comprueba antes que `gh auth status` funciona. Tras publicar, dale al usuario la URL.

## Tarea 3 — Ajustar la guía al repo del usuario

En `deploy/INSTALAR.md`, paso 5, sustituye el clon del original + `git checkout` + `git am` por un clon directo del repo del usuario (`git clone <su URL> ~/staging/whatsapp-mcp`). Si el repo es privado, añade cómo autenticarse en la VM (por ejemplo, una deploy key de solo lectura o `gh auth login`). Elimina la línea del `scp` del parche. Haz commit y, con confirmación, push.

## Tarea 4 — Preparar Azure y Tailscale (con el usuario)

1. Pide al usuario que haga en el panel de Tailscale lo del paso 1 de la guía (MagicDNS, HTTPS, política de acceso con su email, clave de un solo uso con `tag:whatsapp`). Tú no tocas el panel.
2. Copia `deploy/azure-cloud-init.yaml` a `~/whatsapp-cloud-init.yaml` (fuera del repo) y pide al usuario que pegue ahí su clave. No la leas en voz alta ni la repitas en el chat.
3. Comprueba que `az account show` funciona y pregunta la región.
4. Muestra los comandos `az group create` y `az vm create` del paso 2 (con `--custom-data ~/whatsapp-cloud-init.yaml`). **Ejecútalos solo tras confirmación explícita.**
5. Cuando la VM aparezca en Tailscale, el usuario hará los pasos 3 a 10 de la guía en la VM. Acompáñalo paso a paso: dale los comandos de cada paso, pídele que te pegue la salida si algo falla y ayúdalo a resolverlo. Insiste en el paso 3 (quitar el `sudo` sin contraseña).

## Tarea 5 — Conectar Claude Code y verificar

1. Pide al usuario el nombre completo de la VM en la tailnet y registra el MCP:
   `claude mcp add --transport http --scope user whatsapp https://<vm>.<tailnet>.ts.net/mcp`
2. Dale el comando para instalar `deploy/managed-settings.json` como configuración gestionada según su sistema operativo (paso 11 de la guía). Lo ejecuta él con permisos de administrador.
3. Recorre con él las comprobaciones del paso 12 y anota el resultado de cada una. Las más importantes:
   - Un `curl` al puente desde la VM a un número no permitido devuelve "Recipient not allowed".
   - `/lista` desde WhatsApp Web no recibe respuesta (solo vale desde el móvil).
   - Su usuario de la VM no puede listar `/opt/whatsapp-mcp/whatsapp-bridge/store`.
   - Desde Claude Code solo aparecen los chats permitidos y un envío a un número no permitido falla.
4. Termina con un resumen: qué quedó instalado, dónde, cómo cambiar las listas y cómo volver a vincular el móvil cada ~20 días.

## Si algo falla

- **El puente no conecta y WhatsApp dice que el cliente está desactualizado**: hay que actualizar whatsmeow (`go get go.mau.fi/whatsmeow@latest && go mod tidy`), corregir los cambios de API que aparezcan y volver a pasar los tests. Explícale al usuario qué cambiaste.
- **El MCP devuelve 421**: `WA_MCP_EXTRA_HOSTS` en `/etc/default/whatsapp-mcp` no coincide con el nombre de la tailnet.
- **Los mensajes de un contacto permitido no aparecen**: puede que WhatsApp use un ID `@lid` para ese chat; el puente lo trata como no permitido a propósito. Revisa los logs (`journalctl -u whatsapp-bridge`) con el usuario antes de cambiar nada.
