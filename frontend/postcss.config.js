// Tailwind 必须通过 PostCSS 插件生效：
// 缺了这个文件，src/index.css 里的 @tailwind 指令会被原样输出，
// 构建产物 CSS 里没有任何 utility，页面就会表现为「没有样式」。
export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
};
