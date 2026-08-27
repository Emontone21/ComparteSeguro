// Pantalla de creación: cifrar la nota y pedirle un identificador al servidor.

import { hayWebCrypto, aBase64Url, cifrar, copiar } from "/estatico/cripto.js";

const $ = (id) => document.getElementById(id);

const limiteBytes = Number(document.body.dataset.limiteBytes) || 100 * 1024;

const texto      = $("texto");
const contador   = $("contador");
const generar    = $("generar");
const errorCrear = $("error-crear");
const pasoRedactar = $("paso-redactar");
const pasoEnlace   = $("paso-enlace");
const campoEnlace  = $("enlace");
const botonCopiar  = $("copiar");
const avisoCopia   = $("aviso-copia");
const botonOtra    = $("otra");

const codificador = new TextEncoder();

function formatearKB(bytes) {
  return (bytes / 1024).toFixed(bytes < 10240 ? 1 : 0);
}

function actualizarContador() {
  const usados = codificador.encode(texto.value).length;
  const excedido = usados > limiteBytes;
  contador.textContent = `${formatearKB(usados)} / ${formatearKB(limiteBytes)} KB`;
  contador.classList.toggle("contador-excedido", excedido);
  generar.disabled = excedido || texto.value.length === 0;
}

function mostrarError(mensaje) {
  errorCrear.textContent = mensaje;
  errorCrear.classList.remove("oculto");
}

function limpiarError() {
  errorCrear.textContent = "";
  errorCrear.classList.add("oculto");
}

async function crearNota() {
  limpiarError();

  const contenido = texto.value;
  if (contenido.length === 0) return;

  const usados = codificador.encode(contenido).length;
  if (usados > limiteBytes) {
    mostrarError(`La nota supera el límite de ${formatearKB(limiteBytes)} KB.`);
    return;
  }

  generar.disabled = true;
  generar.textContent = "Generando…";

  try {
    // Cifrar primero: al servidor solo le llega el resultado.
    const { bloque, claveB64 } = await cifrar(contenido);

    const respuesta = await fetch("/api/notas", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ contenido: aBase64Url(bloque) }),
    });

    if (!respuesta.ok) {
      const cuerpo = await respuesta.json().catch(() => ({}));
      mostrarError(cuerpo.error || "No se pudo generar el enlace. Intentá de nuevo.");
      return;
    }

    const { id } = await respuesta.json();

    // La clave se agrega acá, en el navegador. Nunca formó parte de ninguna
    // petición al servidor y nunca lo hará.
    campoEnlace.value = `${window.location.origin}/n/${id}#${claveB64}`;

    // Borrar el texto en claro de la pantalla en cuanto deja de hacer falta.
    texto.value = "";
    actualizarContador();

    pasoRedactar.classList.add("oculto");
    pasoEnlace.classList.remove("oculto");
    campoEnlace.focus();
    campoEnlace.select();
    // Seleccionar deja el campo desplazado hasta el final, o sea mostrando un
    // tramo de la clave. Se lo vuelve al principio para que se vea la URL.
    campoEnlace.scrollLeft = 0;
  } catch (e) {
    mostrarError("No se pudo generar el enlace. Revisá la conexión e intentá de nuevo.");
  } finally {
    generar.disabled = false;
    generar.textContent = "Generar enlace";
  }
}

async function copiarEnlace() {
  try {
    await copiar(campoEnlace.value);
    avisoCopia.textContent = "Enlace copiado al portapapeles.";
    avisoCopia.classList.remove("oculto");
  } catch (e) {
    avisoCopia.textContent = "No se pudo copiar automáticamente. Copialo a mano desde el campo de arriba.";
    avisoCopia.classList.remove("oculto");
    campoEnlace.focus();
    campoEnlace.select();
  }
}

function empezarDeNuevo() {
  campoEnlace.value = "";
  avisoCopia.classList.add("oculto");
  pasoEnlace.classList.add("oculto");
  pasoRedactar.classList.remove("oculto");
  texto.focus();
}

function iniciar() {
  if (!hayWebCrypto()) {
    $("sin-webcrypto").classList.remove("oculto");
    pasoRedactar.classList.add("oculto");
    return;
  }

  actualizarContador();
  texto.addEventListener("input", actualizarContador);
  generar.addEventListener("click", crearNota);
  botonCopiar.addEventListener("click", copiarEnlace);
  botonOtra.addEventListener("click", empezarDeNuevo);
}

iniciar();
