// Pantalla de lectura: advertir, y recién al confirmar pedir la nota y
// descifrarla. Cargar esta página no consume nada; el consumo lo dispara
// únicamente el botón.

import { hayWebCrypto, desdeBase64Url, importarClave, descifrar, copiar } from "/estatico/cripto.js";

const $ = (id) => document.getElementById(id);

const MENSAJE_GENERICO = "Esta nota ya fue leída o no existe";

const pasoAviso  = $("paso-aviso");
const pasoNota   = $("paso-nota");
const pasoError  = $("paso-error");
const botonVer   = $("ver");
const contenido  = $("contenido");
const botonCopiar = $("copiar");
const avisoCopia  = $("aviso-copia");
const mensajeError = $("mensaje-error");

function mostrarError(mensaje) {
  mensajeError.textContent = mensaje;
  pasoAviso.classList.add("oculto");
  pasoNota.classList.add("oculto");
  pasoError.classList.remove("oculto");
}

/** El identificador es el último segmento de la ruta: /n/<id> */
function identificadorDeLaURL() {
  const partes = window.location.pathname.split("/");
  return partes[partes.length - 1] || "";
}

/** La clave viaja después del # y nunca sale del navegador. */
function claveDeLaURL() {
  return window.location.hash.startsWith("#") ? window.location.hash.slice(1) : "";
}

// Al terminar de leer una nota se le saca la clave a la barra de direcciones.
// Eso deja una ambigüedad: si después se recarga la página, no hay forma de
// saber si el enlace llegó cortado o si es esta misma pestaña volviendo sobre
// una nota que ya leyó. Se deja una marca para distinguirlas y poder dar el
// mensaje correcto. Dura lo que dura la pestaña y no guarda ningún secreto: la
// nota, a esa altura, ya no existe.
const PREFIJO_MARCA = "comparteseguro:leida:";

function marcarComoLeida(id) {
  try {
    sessionStorage.setItem(PREFIJO_MARCA + id, "1");
  } catch (e) {
    /* si el navegador no deja, se pierde el matiz del mensaje y nada más */
  }
}

function yaSeLeyoEnEstaPestana(id) {
  try {
    return sessionStorage.getItem(PREFIJO_MARCA + id) === "1";
  } catch (e) {
    return false;
  }
}

async function verNota() {
  botonVer.disabled = true;
  botonVer.textContent = "Abriendo…";

  const id = identificadorDeLaURL();
  const claveB64 = claveDeLaURL();

  // Validar la clave ANTES de pedir la nota. Si el enlace llegó cortado (a
  // varios clientes de correo se les va la mano con las URL largas) más vale
  // avisar ahora que destruir la nota y no poder mostrarla.
  let clave;
  try {
    clave = await importarClave(claveB64);
  } catch (e) {
    mostrarError("El enlace está incompleto o dañado. La nota sigue intacta: pedí que te lo reenvíen entero.");
    return;
  }

  let bloque;
  try {
    // POST, no GET: así los escáneres de enlaces de los clientes de correo y
    // de mensajería no queman la nota con solo visitarla.
    const respuesta = await fetch(`/api/notas/${encodeURIComponent(id)}/consumir`, {
      method: "POST",
      headers: { "Accept": "application/json" },
    });

    if (!respuesta.ok) {
      mostrarError(MENSAJE_GENERICO);
      return;
    }

    const cuerpo = await respuesta.json();
    bloque = desdeBase64Url(cuerpo.contenido);
  } catch (e) {
    // No se sabe si el servidor llegó a borrarla, así que no se promete nada.
    botonVer.disabled = false;
    botonVer.textContent = "Ver nota";
    mostrarError("No se pudo conectar con el servidor. Si el problema sigue, pedí un enlace nuevo.");
    return;
  }

  // A partir de acá la nota ya no existe en el servidor.
  let enClaro;
  try {
    enClaro = await descifrar(bloque, clave);
  } catch (e) {
    mostrarError("La nota se destruyó pero no se pudo descifrar: la clave del enlace no corresponde. Pedí un enlace nuevo.");
    return;
  }

  contenido.textContent = enClaro;
  pasoAviso.classList.add("oculto");
  pasoNota.classList.remove("oculto");
  contenido.focus();

  // Sacar la clave de la barra de direcciones y del historial. La nota ya está
  // destruida, así que el enlace no sirve para nada más.
  marcarComoLeida(id);
  try {
    window.history.replaceState(null, "", window.location.pathname);
  } catch (e) {
    /* si el navegador no deja, no es grave */
  }
}

async function copiarContenido() {
  try {
    await copiar(contenido.textContent);
    avisoCopia.textContent = "Contenido copiado al portapapeles.";
  } catch (e) {
    avisoCopia.textContent = "No se pudo copiar automáticamente. Seleccioná el texto y copialo a mano.";
  }
  avisoCopia.classList.remove("oculto");
}

function iniciar() {
  if (!hayWebCrypto()) {
    $("sin-webcrypto").classList.remove("oculto");
    pasoAviso.classList.add("oculto");
    return;
  }

  if (claveDeLaURL() === "") {
    mostrarError(
      yaSeLeyoEnEstaPestana(identificadorDeLaURL())
        ? MENSAJE_GENERICO
        : "El enlace está incompleto: le falta la clave que viene después del signo #.",
    );
    return;
  }

  botonVer.addEventListener("click", verNota);
  botonCopiar.addEventListener("click", copiarContenido);
}

iniciar();
