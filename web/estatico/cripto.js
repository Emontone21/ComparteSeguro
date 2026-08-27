// Cifrado del lado del cliente.
//
// Todo lo sensible ocurre acá dentro: la clave se genera en el navegador, el
// texto se cifra antes de salir a la red, y el servidor solo recibe el
// resultado. La clave nunca se envía: viaja en el fragmento de la URL, que los
// navegadores no incluyen en la petición HTTP.
//
// Formato del bloque que se guarda en el servidor:
//   [ 12 bytes de vector de inicialización ][ texto cifrado + etiqueta GCM ]

const LARGO_IV = 12;   // 96 bits, el tamaño recomendado para AES-GCM
const LARGO_CLAVE = 32; // 256 bits

/** ¿Está disponible WebCrypto? Solo lo está en contextos seguros (HTTPS o localhost). */
export function hayWebCrypto() {
  return typeof crypto !== "undefined" &&
         typeof crypto.subtle !== "undefined" &&
         typeof crypto.getRandomValues === "function";
}

/** Codifica bytes en base64url sin relleno, apto para URL. */
export function aBase64Url(bytes) {
  let binario = "";
  for (const b of bytes) binario += String.fromCharCode(b);
  return btoa(binario).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Decodifica base64url a bytes. Lanza si la entrada no es válida. */
export function desdeBase64Url(texto) {
  const normalizado = texto.replace(/-/g, "+").replace(/_/g, "/");
  const relleno = normalizado + "=".repeat((4 - (normalizado.length % 4)) % 4);
  const binario = atob(relleno);
  const bytes = new Uint8Array(binario.length);
  for (let i = 0; i < binario.length; i++) bytes[i] = binario.charCodeAt(i);
  return bytes;
}

/**
 * Cifra un texto con una clave AES-256-GCM recién generada.
 * Devuelve el bloque a guardar y la clave en base64url, que es lo que se pone
 * en el fragmento del enlace.
 */
export async function cifrar(texto) {
  const clave = await crypto.subtle.generateKey(
    { name: "AES-GCM", length: 256 },
    true,                 // exportable: hace falta para ponerla en el enlace
    ["encrypt"],
  );

  const iv = crypto.getRandomValues(new Uint8Array(LARGO_IV));
  const enClaro = new TextEncoder().encode(texto);
  const cifrado = new Uint8Array(
    await crypto.subtle.encrypt({ name: "AES-GCM", iv }, clave, enClaro),
  );

  const bloque = new Uint8Array(iv.length + cifrado.length);
  bloque.set(iv, 0);
  bloque.set(cifrado, iv.length);

  const bytesClave = new Uint8Array(await crypto.subtle.exportKey("raw", clave));

  return { bloque, claveB64: aBase64Url(bytesClave) };
}

/**
 * Reconstruye la clave a partir del fragmento del enlace.
 * Se llama ANTES de pedirle la nota al servidor: si el enlace está incompleto
 * conviene enterarse antes de destruir la nota, no después.
 */
export async function importarClave(claveB64) {
  const bytes = desdeBase64Url(claveB64);
  if (bytes.length !== LARGO_CLAVE) {
    throw new Error("la clave del enlace no tiene el tamaño esperado");
  }
  return crypto.subtle.importKey("raw", bytes, { name: "AES-GCM" }, false, ["decrypt"]);
}

/** Descifra un bloque con una clave ya importada. */
export async function descifrar(bloque, clave) {
  if (bloque.length <= LARGO_IV) {
    throw new Error("el contenido cifrado es demasiado corto");
  }
  const iv = bloque.subarray(0, LARGO_IV);
  const cuerpo = bloque.subarray(LARGO_IV);
  const enClaro = await crypto.subtle.decrypt({ name: "AES-GCM", iv }, clave, cuerpo);
  return new TextDecoder().decode(enClaro);
}

/** Copia texto al portapapeles, con alternativa para navegadores viejos. */
export async function copiar(texto) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(texto);
    return;
  }
  const temporal = document.createElement("textarea");
  temporal.value = texto;
  temporal.setAttribute("readonly", "");
  temporal.style.position = "fixed";
  temporal.style.opacity = "0";
  document.body.appendChild(temporal);
  temporal.select();
  try {
    if (!document.execCommand("copy")) throw new Error("el navegador rechazó la copia");
  } finally {
    document.body.removeChild(temporal);
  }
}
