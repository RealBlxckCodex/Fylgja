declare module '@novnc/novnc'
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const c: DefineComponent<object, object, any>
  export default c
}
