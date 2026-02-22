/* Material Design 3 Ripple Effect */

document.addEventListener('DOMContentLoaded', () => {
    // Add ripple to all buttons
    document.querySelectorAll('.btn, .icon-btn, .fab').forEach(button => {
        button.classList.add('ripple');
    });
});
