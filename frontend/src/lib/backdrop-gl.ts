export interface BackdropDrawParams {
  time: number;
  blend: number;
  curSlot: 0 | 1;
  intensity?: number;
  scale?: number;
  saturation?: number;
  dim?: number;
}

export interface BackdropGL {
  setTexture(slot: 0 | 1, image: TexImageSource): void;
  resize(width: number, height: number): void;
  draw(params: BackdropDrawParams): void;
  dispose(): void;
}

export interface BackdropGLOptions {
  onContextLost?: () => void;
  onContextRestored?: () => void;
}

const VERTEX_SRC = `#version 300 es
out vec2 v_uv;
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  v_uv = vec2(p.x, 1.0 - p.y);
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}
`;

// Animated album-art backdrop shader ported from ytm-rs (src/shader.rs,
// https://github.com/grapedjuice/ytm-rs, GPL-3.0-only), which is based on
// Kawarp (MIT, Copyright (c) 2026 Better Lyrics).
// Adapted for WebGL2 in MoLow (Copyright (C) 2026 Asrian Putra).
const FRAGMENT_SRC = `#version 300 es
precision highp float;
uniform sampler2D u_a;
uniform sampler2D u_b;
uniform float u_blend;
uniform float u_time;
uniform float u_intensity;
uniform float u_scale;
uniform float u_saturation;
uniform float u_dim;
uniform vec2 u_resolution;
in vec2 v_uv;
out vec4 frag_color;

vec3 mod289(vec3 x) { return x - floor(x * (1.0 / 289.0)) * 289.0; }
vec2 mod289(vec2 x) { return x - floor(x * (1.0 / 289.0)) * 289.0; }
vec3 permute(vec3 x) { return mod289(((x * 34.0) + 1.0) * x); }
float snoise(vec2 v) {
  const vec4 C = vec4(0.211324865405187, 0.366025403784439, -0.577350269189626, 0.024390243902439);
  vec2 i = floor(v + dot(v, C.yy));
  vec2 x0 = v - i + dot(i, C.xx);
  vec2 i1 = (x0.x > x0.y) ? vec2(1.0, 0.0) : vec2(0.0, 1.0);
  vec4 x12 = x0.xyxy + C.xxzz;
  x12.xy -= i1;
  i = mod289(i);
  vec3 p = permute(permute(i.y + vec3(0.0, i1.y, 1.0)) + i.x + vec3(0.0, i1.x, 1.0));
  vec3 m = max(0.5 - vec3(dot(x0, x0), dot(x12.xy, x12.xy), dot(x12.zw, x12.zw)), 0.0);
  m = m * m;
  m = m * m;
  vec3 x = 2.0 * fract(p * C.www) - 1.0;
  vec3 h = abs(x) - 0.5;
  vec3 ox = floor(x + 0.5);
  vec3 a0 = x - ox;
  m *= 1.79284291400159 - 0.85373472095314 * (a0 * a0 + h * h);
  vec3 g;
  g.x = a0.x * x0.x + h.x * x0.y;
  g.yz = a0.yz * x12.xz + h.yz * x12.yw;
  return 130.0 * dot(m, g);
}
float hash(vec3 p) {
  p = fract(p * 0.1031);
  p += dot(p, p.zyx + 31.32);
  return fract((p.x + p.y) * p.z);
}

void main() {
  vec2 uv = (v_uv - 0.5) / u_scale + 0.5;
  float t = u_time * 0.05;
  float centerWeight = 1.0 - smoothstep(0.0, 0.7, length(uv - 0.5));
  float n1 = snoise(uv * 0.35 + vec2(t, t * 0.7));
  float n2 = snoise(uv * 0.35 + vec2(-t * 0.8, t * 0.5) + vec2(50.0, 50.0));
  float n3 = snoise(uv * 0.9 + vec2(t * 1.2, -t) + vec2(100.0, 0.0));
  float n4 = snoise(uv * 0.9 + vec2(-t, t * 1.1) + vec2(0.0, 100.0));
  vec2 warp = vec2(n1 * 0.65 + n3 * 0.35, n2 * 0.65 + n4 * 0.35) * centerWeight;
  vec2 w = clamp(uv + warp * u_intensity, 0.0, 1.0);

  vec3 color = mix(texture(u_b, w).rgb, texture(u_a, w).rgb, u_blend);

  vec2 c = v_uv - 0.5;
  color *= 1.0 - dot(c, c) * 0.3;
  float gray = dot(color, vec3(0.299, 0.587, 0.114));
  color = mix(vec3(gray), color, u_saturation);
  color *= u_dim;
  color += (hash(vec3(floor(v_uv * u_resolution), floor(u_time * 30.0))) - 0.5) / 255.0 * 1.5;
  frag_color = vec4(color, 1.0);
}
`;

export const DEFAULT_INTENSITY = 0.25;
export const DEFAULT_SCALE = 1.25;
export const DEFAULT_SATURATION = 1.35;
export const DEFAULT_DIM = 0.85;

/**
 * Computes the internal WebGL backing-store dimensions capped at 320..960 px
 * on the longest side (~half CSS pixels) so fragment cost stays bounded on a
 * 64x64 blurred source texture.
 */
export function clampBackdropSize(
  cssWidth: number,
  cssHeight: number,
  dpr = 1,
): { width: number; height: number } {
  const safeW = Math.max(1, cssWidth);
  const safeH = Math.max(1, cssHeight);
  const scale = Math.min(Math.max(dpr, 1), 1.5) * 0.5;
  const aspect = safeW / safeH;

  let width: number;
  let height: number;
  if (safeW >= safeH) {
    width = Math.min(960, Math.max(320, Math.round(safeW * scale)));
    height = Math.max(1, Math.round(width / aspect));
  } else {
    height = Math.min(960, Math.max(320, Math.round(safeH * scale)));
    width = Math.max(1, Math.round(height * aspect));
  }
  return { width, height };
}

function compileShader(gl: WebGL2RenderingContext, type: number, source: string): WebGLShader | null {
  const shader = gl.createShader(type);
  if (!shader) return null;
  gl.shaderSource(shader, source);
  gl.compileShader(shader);
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    gl.deleteShader(shader);
    return null;
  }
  return shader;
}

function createTexture(gl: WebGL2RenderingContext): WebGLTexture | null {
  const tex = gl.createTexture();
  if (!tex) return null;
  gl.bindTexture(gl.TEXTURE_2D, tex);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  // Seed with a 1x1 dark neutral pixel so sampling before image load is defined.
  gl.texImage2D(
    gl.TEXTURE_2D,
    0,
    gl.RGBA,
    1,
    1,
    0,
    gl.RGBA,
    gl.UNSIGNED_BYTE,
    new Uint8Array([36, 36, 40, 255]),
  );
  return tex;
}

export function createBackdropGL(
  canvas: HTMLCanvasElement,
  opts: BackdropGLOptions = {},
): BackdropGL | null {
  const gl = canvas.getContext("webgl2", {
    alpha: false,
    depth: false,
    stencil: false,
    antialias: false,
    powerPreference: "low-power",
    preserveDrawingBuffer: false,
  });
  if (!gl) return null;

  const vs = compileShader(gl, gl.VERTEX_SHADER, VERTEX_SRC);
  const fs = compileShader(gl, gl.FRAGMENT_SHADER, FRAGMENT_SRC);
  if (!vs || !fs) {
    if (vs) gl.deleteShader(vs);
    if (fs) gl.deleteShader(fs);
    return null;
  }

  const program = gl.createProgram();
  if (!program) {
    gl.deleteShader(vs);
    gl.deleteShader(fs);
    return null;
  }
  gl.attachShader(program, vs);
  gl.attachShader(program, fs);
  gl.linkProgram(program);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    gl.deleteProgram(program);
    gl.deleteShader(vs);
    gl.deleteShader(fs);
    return null;
  }

  const tex0 = createTexture(gl);
  const tex1 = createTexture(gl);
  if (!tex0 || !tex1) {
    if (tex0) gl.deleteTexture(tex0);
    if (tex1) gl.deleteTexture(tex1);
    gl.deleteProgram(program);
    gl.deleteShader(vs);
    gl.deleteShader(fs);
    return null;
  }
  const textures: [WebGLTexture, WebGLTexture] = [tex0, tex1];

  const locA = gl.getUniformLocation(program, "u_a");
  const locB = gl.getUniformLocation(program, "u_b");
  const locBlend = gl.getUniformLocation(program, "u_blend");
  const locTime = gl.getUniformLocation(program, "u_time");
  const locIntensity = gl.getUniformLocation(program, "u_intensity");
  const locScale = gl.getUniformLocation(program, "u_scale");
  const locSaturation = gl.getUniformLocation(program, "u_saturation");
  const locDim = gl.getUniformLocation(program, "u_dim");
  const locResolution = gl.getUniformLocation(program, "u_resolution");

  let disposed = false;
  let contextLost = false;
  let width = Math.max(1, canvas.width);
  let height = Math.max(1, canvas.height);

  const onLost = (e: Event) => {
    e.preventDefault();
    contextLost = true;
    opts.onContextLost?.();
  };
  const onRestored = () => {
    contextLost = false;
    opts.onContextRestored?.();
  };
  canvas.addEventListener("webglcontextlost", onLost);
  canvas.addEventListener("webglcontextrestored", onRestored);

  return {
    setTexture(slot, image) {
      if (disposed || contextLost) return;
      gl.bindTexture(gl.TEXTURE_2D, textures[slot]);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, image);
    },
    resize(w, h) {
      if (disposed) return;
      width = Math.max(1, Math.round(w));
      height = Math.max(1, Math.round(h));
      if (canvas.width !== width) canvas.width = width;
      if (canvas.height !== height) canvas.height = height;
      if (!contextLost) {
        gl.viewport(0, 0, width, height);
      }
    },
    draw(params) {
      if (disposed || contextLost) return;
      const cur = params.curSlot;
      const prev: 0 | 1 = cur === 0 ? 1 : 0;

      gl.viewport(0, 0, width, height);
      gl.useProgram(program);

      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, textures[cur]);
      gl.uniform1i(locA, 0);

      gl.activeTexture(gl.TEXTURE1);
      gl.bindTexture(gl.TEXTURE_2D, textures[prev]);
      gl.uniform1i(locB, 1);

      gl.uniform1f(locBlend, params.blend);
      gl.uniform1f(locTime, params.time);
      gl.uniform1f(locIntensity, params.intensity ?? DEFAULT_INTENSITY);
      gl.uniform1f(locScale, params.scale ?? DEFAULT_SCALE);
      gl.uniform1f(locSaturation, params.saturation ?? DEFAULT_SATURATION);
      gl.uniform1f(locDim, params.dim ?? DEFAULT_DIM);
      gl.uniform2f(locResolution, width, height);

      gl.drawArrays(gl.TRIANGLES, 0, 3);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      canvas.removeEventListener("webglcontextlost", onLost);
      canvas.removeEventListener("webglcontextrestored", onRestored);
      if (!gl.isContextLost()) {
        gl.deleteTexture(textures[0]);
        gl.deleteTexture(textures[1]);
        gl.deleteProgram(program);
        gl.deleteShader(vs);
        gl.deleteShader(fs);
      }
    },
  };
}
