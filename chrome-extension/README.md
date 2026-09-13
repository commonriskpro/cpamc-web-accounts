# CPAMC Web Accounts · extensión Chrome

Extensión MV3 sin dependencias ni bundles remotos. La versión `0.2.0` abre un
panel lateral dentro de la pestaña CPAMC activa; no crea una segunda pantalla
de conexión ni pide una segunda clave de Management. Requiere Chrome 106 o
posterior para vincular también el documento de la pestaña.

> Estado: flujo integrado probado con datos ficticios y dobles de Chrome. No
> se han usado cuentas reales, no se ha instalado en un navegador del usuario
> y la inferencia de los proveedores no se valida aquí.

## Instalar sin publicar

1. Abre `chrome://extensions`, activa **Developer mode** y pulsa **Load
   unpacked**.
2. Selecciona esta carpeta `chrome-extension/`.
3. Abre CPAMC en la pestaña que quieras usar, en la ruta
   `/management.html` (por ejemplo `http://127.0.0.1:8317/management.html`).
4. Pulsa la acción **Conectar cuentas web**. La extensión solicita `scripting`
   y el permiso de host exacto para el origen visible; si se deniega, no abre
   el panel ni captura nada.

El panel queda dentro de la misma página, en un contenedor Shadow DOM abierto
para accesibilidad y automatización,
y muestra el origen fijado de esa pestaña. No depende de selectores ni del DOM
interno de CPAMC. Para vincular la sesión, pulsa **Usar sesión de CPAMC** y
después **Refresh All** en CPAMC. La extensión observa una única petición XHR
GET de Management del mismo origen y solo su cabecera `Authorization: Bearer`.
El hook se restaura al terminar, cancelar o tras 60 segundos; no lee cuerpos,
otras cabeceras, formularios ni `localStorage` de CPAMC.

La clave capturada se valida contra `/v0/management/plugins` y solo entonces se
guarda en un único registro `chrome.storage.session` durante 30 minutos. La
clave se acepta durante ese periodo; al siguiente acceso después de expirar se
elimina, y Chrome también puede limpiar `storage.session` al cerrar o reiniciar.
La URL no se copia desde un formulario ni se guarda como secreto. **Desconectar
esta sesión** cancela una captura pendiente y borra la clave.

## Conectores

- **Claude:** solicita permiso solo al pulsar su botón y lee únicamente la
  cookie `sessionKey` de `https://claude.ai`.
- **Kimi:** solicita permiso solo al pulsar su botón, busca solo
  `https://www.kimi.ai`, lee `localStorage.access_token` en mundo aislado y
  usa `kimi-auth` como fallback. `kimi.com` nunca se consulta.
- **Qoder / Qwen3.8:** no captura una sesión web. Acepta un PAT introducido
  manualmente para el conector CLI; Qwen Web no está implementado.

Cada acción de proveedor es explícita y comprueba que la pestaña, documento,
origen y activación sigan siendo los mismos antes de capturar y antes de subir.
El JSON `type: "web-accounts"` se envía al endpoint Auth Files del CPAMC con
un nombre UUID nuevo. No hay reintentos automáticos. Un POST, redirect,
respuesta inválida o timeout incierto exige verificar Auth Files antes de
reintentar. No se escriben claves, cookies ni PAT en URLs, logs, DOM,
`postMessage` o `chrome.storage.local`.

El acceso HTTP solo se permite para `localhost`, `127.0.0.1` y `[::1]`; los
orígenes HTTPS son configurables, pero siempre se fija el origen exacto de la
pestaña activada. No se registran content scripts ni listeners externos.

## Pruebas

```sh
npm test
node --check core.js
node --check panel.js
node --check background.js
node --check page-hook.js
python3 -m json.tool manifest.json >/dev/null
```

Las pruebas usan dobles inyectables para `chrome.*`, `fetch` y el hook XHR; no
extraen cookies reales ni contactan cuentas.
